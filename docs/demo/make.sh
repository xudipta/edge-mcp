#!/usr/bin/env bash
# Regenerates docs/demo.gif and docs/demo.mp4 from the real --demo server.
# Needs: go, python3 with Pillow, ffmpeg, and DejaVu Sans Mono fonts.
set -euo pipefail
cd "$(dirname "$0")/../.."
export DEMO_OUT="${DEMO_OUT:-/tmp/edge-mcp-demo}"
make build
python3 docs/demo/capture.py
python3 docs/demo/render.py
f="$DEMO_OUT/frames"
ffmpeg -loglevel error -y -framerate 12 -i "$f/f%05d.png" \
  -vf "split[a][b];[a]palettegen=max_colors=48:stats_mode=diff[p];[b][p]paletteuse=dither=none" docs/demo.gif
ffmpeg -loglevel error -y -framerate 12 -i "$f/f%05d.png" -c:v libx264 -pix_fmt yuv420p -crf 20 -movflags +faststart docs/demo.mp4
ls -l docs/demo.gif docs/demo.mp4
