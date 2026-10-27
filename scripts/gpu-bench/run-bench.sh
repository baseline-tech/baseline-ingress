#!/usr/bin/env bash
# Usage: run-bench.sh N [PRESET] [DURATION_S]
# Env: RUN_ID overrides the output directory name under out/.
set -euo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$DIR/../.." && pwd)

N=${1:?usage: run-bench.sh N [PRESET] [DURATION_S]}
PRESET=${2:-}
DURATION=${3:-60}
RUN_ID=${RUN_ID:-$(date +%Y%m%d-%H%M%S)-n$N-${PRESET:-default}}
RUN_DIR=$DIR/out/$RUN_ID

export LIVEKIT_URL=${LIVEKIT_URL:-http://localhost:7880}
export LIVEKIT_API_KEY=${LIVEKIT_API_KEY:-devkey}
export LIVEKIT_API_SECRET=${LIVEKIT_API_SECRET:-secret}
export GSTVERSION=${GSTVERSION:-$(cat "$ROOT/.gst-version")}

PUB_PIDS=()
SAMPLER_PIDS=()
INGRESS_IDS=()
RUN_START=
DROPS=0
FALLBACKS=0
BACKENDS=none

compose() { docker compose -f "$DIR/docker-compose.yml" "$@"; }

cleanup() {
  local pid id
  trap - EXIT
  for pid in "${PUB_PIDS[@]}" "${SAMPLER_PIDS[@]}"; do
    kill "$pid" 2>/dev/null || true
  done
  for id in "${INGRESS_IDS[@]}"; do
    lk ingress delete "$id" >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT

wait_for_http() {
  local url=$1 tries=${2:-60}
  while ((tries-- > 0)); do
    curl -fs -o /dev/null "$url" && return 0
    sleep 1
  done
  echo "timed out waiting for $url" >&2
  return 1
}

# Rewrites in place: a single-file bind mount keeps the original inode, so mv would not reach the container.
set_preset() {
  local tmp
  tmp=$(mktemp)
  sed -E "s/^([[:space:]]*preset:).*/\1 $1/" "$DIR/ingress.yaml" >"$tmp"
  cat "$tmp" >"$DIR/ingress.yaml"
  rm -f "$tmp"
}

current_preset() {
  sed -En 's/^[[:space:]]*preset:[[:space:]]*([A-Za-z0-9]+).*/\1/p' "$DIR/ingress.yaml"
}

start_stack() {
  compose up -d redis livekit
  wait_for_http http://localhost:7880
  compose up -d --force-recreate ingress
  wait_for_http http://localhost:8080
}

audio_encoder() {
  local enc
  for enc in avenc_aac voaacenc; do
    if gst-inspect-1.0 "$enc" >/dev/null 2>&1; then
      echo "$enc"
      return 0
    fi
  done
  echo "no AAC encoder (avenc_aac or voaacenc) found" >&2
  return 1
}

request_json() {
  local name=$1
  cat <<JSON
{
  "input_type": "RTMP_INPUT",
  "name": "$name",
  "room_name": "$name",
  "participant_identity": "$name",
  "participant_name": "$name",
  "video": {
    "options": {
      "video_codec": "H264_BASELINE",
      "frame_rate": 30,
      "layers": [
        {"quality": "HIGH", "width": 1280, "height": 720, "bitrate": 1700000},
        {"quality": "MEDIUM", "width": 640, "height": 360, "bitrate": 700000},
        {"quality": "LOW", "width": 320, "height": 180, "bitrate": 200000}
      ]
    }
  }
}
JSON
}

create_ingress() {
  local i=$1
  local out=$RUN_DIR/ingress-$i.json
  request_json "bench-$i" >"$RUN_DIR/ingress-$i.req.json"
  lk ingress create --json "$RUN_DIR/ingress-$i.req.json" >"$out"
  INGRESS_IDS+=("$(jq -r '.ingress_id' "$out")")
}

publish() {
  local i=$1 aenc=$2 url key
  url=$(jq -r '.url' "$RUN_DIR/ingress-$i.json")
  key=$(jq -r '.stream_key' "$RUN_DIR/ingress-$i.json")
  (
    rc=0
    timeout -s INT "$DURATION" gst-launch-1.0 -e \
      videotestsrc is-live=true pattern=ball \
      ! video/x-raw,width=1920,height=1080,framerate=30/1 \
      ! x264enc tune=zerolatency speed-preset=veryfast bitrate=4500 key-int-max=60 \
      ! h264parse ! flvmux streamable=true name=mux \
      ! rtmp2sink location="$url/$key" \
      audiotestsrc is-live=true ! audioconvert ! "$aenc" ! aacparse ! mux. \
      >"$RUN_DIR/pub-$i.log" 2>&1 || rc=$?
    echo "$rc" >"$RUN_DIR/pub-$i.status"
  ) &
  PUB_PIDS+=("$!")
}

sample_cpu() {
  local cid
  cid=$(compose ps -q ingress)
  while true; do
    echo "$(date +%s) $(docker stats --no-stream --format '{{.CPUPerc}}' "$cid")"
    sleep 2
  done >"$RUN_DIR/cpu.log"
}

start_samplers() {
  nvidia-smi dmon -s pucm -d 1 >"$RUN_DIR/dmon.log" 2>&1 &
  SAMPLER_PIDS+=("$!")
  sample_cpu &
  SAMPLER_PIDS+=("$!")
}

# avg / max of column $1 on stdin; "-" when there is no numeric data
stats() {
  awk -v c="$1" '$c ~ /^[0-9.]+$/ { s += $c; if ($c > m) m = $c; n++ }
    END { if (n) printf "%.1f / %.1f", s / n, m; else print "-" }'
}

# dmon names its columns on the first "#" line; find the one called $1
dmon_stats() {
  awk -v name="$1" '
    /^#/ { if (!h && $0 ~ / enc /) { for (i = 2; i <= NF; i++) if ($i == name) c = i - 1; h = 1 } next }
    h && c && $c ~ /^[0-9.]+$/ { s += $c; if ($c > m) m = $c; n++ }
    END { if (n) printf "%.1f / %.1f", s / n, m; else print "-" }' "$RUN_DIR/dmon.log"
}

cpu_stats() {
  tr -d '%' <"$RUN_DIR/cpu.log" | stats 2
}

scan_logs() {
  local log=$RUN_DIR/ingress.log
  compose logs --no-color --since "$RUN_START" ingress >"$log" 2>&1 || true
  DROPS=$(grep -ciE 'too slow|frame drop|dropped|QoS' "$log" || true)
  FALLBACKS=$(grep -ciE 'gpu transcoding unavailable|falling back' "$log" || true)
  BACKENDS=$({ grep 'video layer' "$log" || true; } \
    | sed -En 's/.*backend"?[=:] ?"?([A-Za-z0-9_-]+).*/\1/p' | sort | uniq -c \
    | awk '{ printf "%s%s x%s", sep, $2, $1; sep = ", " }')
  BACKENDS=${BACKENDS:-none}
}

pub_status() {
  local i ok=0 st
  for ((i = 0; i < N; i++)); do
    st=$(cat "$RUN_DIR/pub-$i.status" 2>/dev/null || echo missing)
    # 124 and 130: stopped by the duration timeout, which is the expected end
    case $st in 0 | 124 | 130) ok=$((ok + 1)) ;; esac
  done
  echo "$ok/$N ok"
}

write_summary() {
  local row
  row="| $N | $(current_preset) | $BACKENDS | $(dmon_stats enc) | $(dmon_stats sm) | $(cpu_stats) | $DROPS | $FALLBACKS | $(pub_status) |"
  {
    echo '| N | preset | backend | enc % avg/max | gpu % avg/max | ingress cpu % avg/max | too-slow/drop | fallback | publishers |'
    echo '|---|---|---|---|---|---|---|---|---|'
    echo "$row"
  } >"$RUN_DIR/summary.md"
  echo "$row" >"$RUN_DIR/row.md"
}

main() {
  local i aenc
  mkdir -p "$RUN_DIR"
  aenc=$(audio_encoder)
  [[ -z $PRESET ]] || set_preset "$PRESET"
  start_stack
  RUN_START=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  for ((i = 0; i < N; i++)); do create_ingress "$i"; done
  start_samplers
  for ((i = 0; i < N; i++)); do publish "$i" "$aenc"; done
  wait "${PUB_PIDS[@]}" || true
  sleep 2
  scan_logs
  write_summary
  cat "$RUN_DIR/summary.md"
}

main
