#!/usr/bin/env bash
# Usage: quality.sh [CLIP]
# Env: PRESETS (default "p1 p3 p5 p7"), BITRATE in kbit (default 1700), CLIP_SECONDS (default 10).
set -euo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
PRESETS=${PRESETS:-p1 p3 p5 p7}
BITRATE=${BITRATE:-1700}
CLIP_SECONDS=${CLIP_SECONDS:-10}
W=1280
H=720
CLIP_URL=https://download.blender.org/demo/movies/BBB/bbb_sunflower_1080p_30fps_normal.mp4
WORK=$DIR/out/quality-$(date +%Y%m%d-%H%M%S)
REF=$WORK/ref.yuv

mkdir -p "$WORK"

prepare_reference() {
  local clip=${1:-}
  if [[ -z $clip ]]; then
    clip=$DIR/out/bbb_sunflower_1080p_30fps_normal.mp4
    [[ -f $clip ]] || curl -fL -o "$clip" "$CLIP_URL"
  fi
  ffmpeg -v error -y -i "$clip" -t "$CLIP_SECONDS" -an \
    -vf "scale=$W:$H,fps=30" -pix_fmt yuv420p -f rawvideo "$REF"
}

now_ms() { echo $(($(date +%s%N) / 1000000)); }

encode() {
  local out=$1 encoder=$2
  # $encoder is a gst element plus properties and must word-split
  # shellcheck disable=SC2086
  gst-launch-1.0 -e -q filesrc location="$REF" \
    ! rawvideoparse format=i420 width=$W height=$H framerate=30/1 \
    ! videoconvert ! $encoder ! h264parse ! mp4mux ! filesink location="$out"
}

score() {
  local encoded=$1 filter log
  if ffmpeg -hide_banner -filters 2>/dev/null | grep -q libvmaf; then
    filter=libvmaf
  else
    filter=psnr
  fi
  log=$(ffmpeg -hide_banner -i "$encoded" \
    -f rawvideo -pix_fmt yuv420p -s "${W}x$H" -r 30 -i "$REF" \
    -lavfi "$filter" -f null - 2>&1)
  if [[ $filter == libvmaf ]]; then
    echo "VMAF $(sed -En 's/.*VMAF score: ([0-9.]+).*/\1/p' <<<"$log" | tail -1)"
  else
    echo "PSNR $(sed -En 's/.*average:([0-9.]+).*/\1/p' <<<"$log" | tail -1) dB"
  fi
}

run_one() {
  local label=$1 encoder=$2 out t0 t1
  out=$WORK/${label// /_}.mp4
  t0=$(now_ms)
  encode "$out" "$encoder"
  t1=$(now_ms)
  echo "| $label | $(score "$out") | $((t1 - t0)) |"
}

main() {
  local p
  prepare_reference "${1:-}"
  {
    echo "| encoder | score | encode wall ms |"
    echo "|---|---|---|"
    for p in $PRESETS; do
      run_one "nvh264enc $p" \
        "nvh264enc preset=$p rc-mode=cbr bitrate=$BITRATE tune=ultra-low-latency zerolatency=true"
    done
    run_one "x264enc veryfast (cpu)" \
      "x264enc tune=zerolatency speed-preset=veryfast bitrate=$BITRATE vbv-buf-capacity=1000"
  } | tee "$WORK/quality.md"
}

main "$@"
