#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${PROJECT_DIR}"

mkdir -p results

CAST_FILE="results/cache-stampede-demo.cast"
MP4_FILE="results/cache-stampede-demo.mp4"
GIF_FILE="results/cache-stampede-demo.gif"

if ! command -v asciinema >/dev/null 2>&1; then
    echo "======================================================================"
    echo "asciinema is not installed."
    echo "======================================================================"
    echo ""
    echo "To install asciinema on macOS via Homebrew, run:"
    echo "  brew install asciinema"
    echo ""
    echo "After installation, run:"
    echo "  make record"
    echo ""
    echo "Alternative: Record your terminal directly using OBS Studio:"
    echo "  - Canvas Resolution: 1920x1080"
    echo "  - Framerate: 30 FPS"
    echo "  - Terminal Font Size: 18-24px"
    echo "  - Run: make demo"
    echo "======================================================================"
    exit 1
fi

echo "Starting asciinema recording to ${CAST_FILE}..."
asciinema rec "${CAST_FILE}" --overwrite -c "./scripts/demo.sh"

echo ""
echo "Recording finished: ${CAST_FILE}"

# Check if MP4 conversion tools are available
if command -v agg >/dev/null 2>&1 && command -v ffmpeg >/dev/null 2>&1; then
    echo "Generating GIF via agg..."
    agg "${CAST_FILE}" "${GIF_FILE}" --speed 1.0 --theme monokai
    echo "Converting GIF to MP4 via ffmpeg..."
    ffmpeg -y -i "${GIF_FILE}" -movflags faststart -pix_fmt yuv420p -vf "scale=trunc(iw/2)*2:trunc(ih/2)*2" "${MP4_FILE}"
    rm -f "${GIF_FILE}"
    echo "Generated MP4 video: ${MP4_FILE}"
else
    echo ""
    echo "To convert ${CAST_FILE} to MP4 video:"
    echo "1. Install agg and ffmpeg:"
    echo "   brew install agg ffmpeg"
    echo "2. Run conversion:"
    echo "   agg ${CAST_FILE} results/cache-stampede-demo.gif"
    echo "   ffmpeg -i results/cache-stampede-demo.gif -movflags faststart -pix_fmt yuv420p results/cache-stampede-demo.mp4"
fi
