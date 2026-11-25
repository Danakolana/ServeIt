#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DOCS="$ROOT/docs"
BIN="$ROOT/ServeIt"
PORT=18781
ADMIN_PASS=adminpass
FIXTURE="$(mktemp -d)"

cleanup() {
  if [[ -n "${PID:-}" ]]; then kill "$PID" 2>/dev/null || true; fi
  rm -rf "$FIXTURE"
}
trap cleanup EXIT

mkdir -p "$DOCS" "$FIXTURE/Photos" "$FIXTURE/Notes"
printf 'shore trip\n' > "$FIXTURE/Photos/vacation.txt"
printf 'meeting notes\n' > "$FIXTURE/Notes/monday.txt"
printf 'name,amount\nrent,1200\n' > "$FIXTURE/budget.csv"
printf 'demo' > "$FIXTURE/clip.mp4"
touch -d '2026-11-02 09:15' "$FIXTURE/Photos/vacation.txt" "$FIXTURE/Photos"
touch -d '2026-11-18 14:02' "$FIXTURE/Notes/monday.txt" "$FIXTURE/Notes"
touch -d '2026-11-20 11:40' "$FIXTURE/budget.csv"
touch -d '2026-11-22 19:05' "$FIXTURE/clip.mp4"

(cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$BIN" .)

STARTUP_LOG="$(mktemp)"
"$BIN" --dir "$FIXTURE" --port "$PORT" --admin-password "$ADMIN_PASS" >"$STARTUP_LOG" 2>&1 &
PID=$!

for _ in $(seq 1 40); do
  if curl -fsS "http://127.0.0.1:$PORT/" >/dev/null 2>&1; then break; fi
  sleep 0.2
done

CHROME=""
for candidate in google-chrome google-chrome-stable chromium chromium-browser chromium-browser; do
  if command -v "$candidate" >/dev/null 2>&1; then CHROME="$candidate"; break; fi
done
if [[ -z "$CHROME" ]]; then
  echo "No headless Chrome found; browser screenshots skipped." >&2
  exit 1
fi

shot() {
  local url="$1" out="$2"
  "$CHROME" --headless=new --disable-gpu --hide-scrollbars \
    --window-size=1320,780 --screenshot="$out" "$url" >/dev/null 2>&1
}

SHARE_JSON="$(
  curl -fsS -u "admin:${ADMIN_PASS}" -X POST "http://127.0.0.1:$PORT/api/admin/shares" \
    -H 'Content-Type: application/json' \
    -d '{"items":["budget.csv","Photos"],"idleTimeoutMinutes":15,"maxLifetimeMinutes":60}'
)"
TOKEN="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["URL"].rstrip("/").split("/")[-1])' "$SHARE_JSON")"

shot "http://127.0.0.1:$PORT/" "$DOCS/file-browser.png"
sleep 0.4
shot "http://admin:${ADMIN_PASS}@127.0.0.1:$PORT/admin" "$DOCS/admin.png"
shot "http://127.0.0.1:$PORT/s/${TOKEN}/" "$DOCS/share.png"

python3 - "$STARTUP_LOG" "$DOCS/file-browser.png" "$DOCS/startup-and-browser.png" <<'PY'
import sys
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont

startup_path, browser_path, out_path = map(Path, sys.argv[1:4])
raw = startup_path.read_text(errors="replace").splitlines()
keep = [line.rstrip() for line in raw]
end = len(keep)
for index in range(len(keep) - 1, -1, -1):
    if any(marker in keep[index] for marker in ("█", "▄", "▀")):
        end = index + 1
        break
else:
    end = min(len(keep), 32)
keep = keep[:end]
while keep and keep[0] == "":
    keep.pop(0)
text = "\n".join(keep).strip() + "\n"

font_path = "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"
font = ImageFont.truetype(font_path, 11)
pad = 16
line_h = 14
lines = text.splitlines() or [""]
term_w = 760
term_h = pad * 2 + line_h * len(lines)
term = Image.new("RGB", (term_w, term_h), "#0f172a")
draw = ImageDraw.Draw(term)
y = pad
for line in lines:
    draw.text((pad, y), line, fill="#e2e8f0", font=font)
    y += line_h

browser = Image.open(browser_path).convert("RGB")
scale = min(1.0, term_h / browser.height)
b_w = int(browser.width * scale)
b_h = int(browser.height * scale)
browser = browser.resize((b_w, b_h), Image.Resampling.LANCZOS)

gap = 12
canvas = Image.new("RGB", (term_w + gap + b_w, max(term_h, b_h)), "#f5f7fa")
canvas.paste(term, (0, (max(term_h, b_h) - term_h) // 2))
canvas.paste(browser, (term_w + gap, (max(term_h, b_h) - b_h) // 2))
canvas.save(out_path, optimize=True)
print(out_path, canvas.size)
PY

echo "Wrote $DOCS/file-browser.png $DOCS/admin.png $DOCS/share.png $DOCS/startup-and-browser.png"