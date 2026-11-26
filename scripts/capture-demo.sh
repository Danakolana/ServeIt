#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
DOCS="$ROOT/docs"
PORT=${SERVEIT_DEMO_PORT:-18782}
CDP_PORT=${SERVEIT_DEMO_CDP_PORT:-19222}
ADMIN_PASS=${SERVEIT_DEMO_ADMIN_PASSWORD:-adminpass}
FIXTURE=$(mktemp -d /tmp/serveit-demo.XXXXXX)
CHROME_PROFILE=$(mktemp -d /tmp/serveit-demo-chrome.XXXXXX)
FRAME_DIR=$(mktemp -d /tmp/serveit-demo-frames.XXXXXX)
BIN_DIR=$(mktemp -d /tmp/serveit-demo-bin.XXXXXX)
BIN="$BIN_DIR/ServeIt"
SERVER_PID=""
CHROME_PID=""

cleanup() {
  [[ -n "$CHROME_PID" ]] && kill "$CHROME_PID" 2>/dev/null || true
  [[ -n "$SERVER_PID" ]] && kill "$SERVER_PID" 2>/dev/null || true
  rm -rf "$FIXTURE" "$CHROME_PROFILE" "$FRAME_DIR" "$BIN_DIR" 2>/dev/null || true
}
trap cleanup EXIT

mkdir -p "$FIXTURE/Project/Photos"
printf 'Project brief\n' > "$FIXTURE/Project/brief.pdf"
printf 'category,total\ntravel,3\n' > "$FIXTURE/Project/budget.csv"
printf 'Bring the signed copy.\n' > "$FIXTURE/Project/notes.txt"
printf 'Unselected draft.\n' > "$FIXTURE/Project/draft.txt"
printf 'Unselected archive.\n' > "$FIXTURE/Project/archive.zip"
printf 'photo placeholder one\n' > "$FIXTURE/Project/Photos/one.jpg"
printf 'photo placeholder two\n' > "$FIXTURE/Project/Photos/two.jpg"

cd "$ROOT"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$BIN" .
"$BIN" --dir "$FIXTURE" --port "$PORT" --admin-password "$ADMIN_PASS" >"$FRAME_DIR/server.log" 2>&1 &
SERVER_PID=$!
for _ in {1..80}; do
  if curl -fsS "http://127.0.0.1:$PORT/" >/dev/null 2>&1; then break; fi
  sleep 0.1
done
curl -fsS "http://127.0.0.1:$PORT/" >/dev/null

google-chrome \
  --headless=new \
  --disable-gpu \
  --no-sandbox \
  --disable-dev-shm-usage \
  --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port="$CDP_PORT" \
  --user-data-dir="$CHROME_PROFILE" \
  about:blank >"$FRAME_DIR/chrome.log" 2>&1 &
CHROME_PID=$!
for _ in {1..80}; do
  if curl -fsS "http://127.0.0.1:$CDP_PORT/json/version" >/dev/null 2>&1; then break; fi
  sleep 0.1
done
curl -fsS "http://127.0.0.1:$CDP_PORT/json/version" >/dev/null

python3 "$ROOT/scripts/capture-demo.py" \
  --base-url "http://127.0.0.1:$PORT" \
  --cdp-port "$CDP_PORT" \
  --admin-password "$ADMIN_PASS" \
  --startup-log "$FRAME_DIR/server.log" \
  --out "$FRAME_DIR"

python3 - "$FRAME_DIR" <<'PY'
from pathlib import Path
import sys

frames = Path(sys.argv[1])
durations = {
    "01-startup.png": 1.2,
    "02-admin.png": 1.2,
    "03-folder.png": 1.1,
    "04-selected.png": 1.4,
    "05-qr.png": 1.6,
    "06-phone.png": 1.5,
    "07-phone-selected.png": 1.3,
    "08-phone-download.png": 1.5,
}
with (frames / "concat.txt").open("w", encoding="utf-8") as out:
    for name, duration in durations.items():
        out.write(f"file '{(frames / name).as_posix()}'\n")
        out.write(f"duration {duration}\n")
    out.write(f"file '{(frames / '08-phone-download.png').as_posix()}'\n")
PY

mkdir -p "$DOCS"
ffmpeg -y -hide_banner -loglevel error \
  -f concat -safe 0 -i "$FRAME_DIR/concat.txt" \
  -vf "fps=8,scale=960:-1:flags=lanczos,split[s0][s1];[s0]palettegen=stats_mode=diff[p];[s1][p]paletteuse=dither=sierra2_4a" \
  -loop 0 "$DOCS/demo.gif"
echo "Wrote $DOCS/demo.gif"
