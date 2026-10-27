#!/usr/bin/env bash
# Usage: [PRESETS="p1 p3 p5 p7"] [NS="1 2 4 8"] [DURATION=60] sweep.sh
set -euo pipefail

DIR=$(cd "$(dirname "$0")" && pwd)
PRESETS=${PRESETS:-p1 p3 p5 p7}
NS=${NS:-1 2 4 8}
DURATION=${DURATION:-60}
STAMP=$(date +%Y%m%d-%H%M%S)
OUT=$DIR/out/sweep-$STAMP.md

mkdir -p "$DIR/out"
{
  echo '| N | preset | backend | enc % avg/max | gpu % avg/max | ingress cpu % avg/max | too-slow/drop | fallback | publishers |'
  echo '|---|---|---|---|---|---|---|---|---|'
} >"$OUT"

for preset in $PRESETS; do
  for n in $NS; do
    id=$STAMP-n$n-$preset
    RUN_ID=$id "$DIR/run-bench.sh" "$n" "$preset" "$DURATION"
    cat "$DIR/out/$id/row.md" >>"$OUT"
  done
done

echo "wrote $OUT"
