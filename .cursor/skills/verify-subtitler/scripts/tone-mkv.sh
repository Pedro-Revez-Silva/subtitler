#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${WORK:-}" ]]; then
  echo "WORK is unset. Set WORK to an empty directory outside the repo root." >&2
  exit 1
fi

if [[ ! -d "$WORK" ]]; then
  echo "WORK is not a directory." >&2
  exit 1
fi

ffmpeg -y -hide_banner -loglevel error \
  -f lavfi -i sine=frequency=440:duration=2 \
  -f lavfi -i testsrc=size=320x240:rate=1:duration=2 \
  -c:v libx264 -pix_fmt yuv420p -c:a aac -shortest \
  "$WORK/tone.mkv"
