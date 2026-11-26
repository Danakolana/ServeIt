#!/usr/bin/env python3
"""Capture the ServeIt workflow used by the README demo GIF.

The script uses Chrome's DevTools Protocol directly so the demo exercises the
real embedded UI and API without adding a browser automation dependency.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import socket
import struct
import time
import urllib.parse
import urllib.request
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont


class WebSocket:
    def __init__(self, url: str) -> None:
        parsed = urllib.parse.urlsplit(url)
        if parsed.scheme != "ws":
            raise ValueError(f"unsupported WebSocket URL: {url}")
        host = parsed.hostname or "127.0.0.1"
        port = parsed.port or 80
        self.sock = socket.create_connection((host, port), timeout=10)
        self.sock.settimeout(10)
        key = base64.b64encode(os.urandom(16)).decode()
        request = (
            f"GET {parsed.path or '/'} HTTP/1.1\r\n"
            f"Host: {host}:{port}\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\n"
            "Sec-WebSocket-Version: 13\r\n\r\n"
        ).encode()
        self.sock.sendall(request)
        response = self._read_until(b"\r\n\r\n")
        if b" 101 " not in response.split(b"\r\n", 1)[0]:
            raise RuntimeError(f"CDP WebSocket handshake failed: {response!r}")
        self.next_id = 0

    def _read_until(self, marker: bytes) -> bytes:
        data = bytearray()
        while marker not in data:
            chunk = self.sock.recv(4096)
            if not chunk:
                raise RuntimeError("CDP connection closed during handshake")
            data.extend(chunk)
        return bytes(data)

    def _read_exact(self, size: int) -> bytes:
        data = bytearray()
        while len(data) < size:
            chunk = self.sock.recv(size - len(data))
            if not chunk:
                raise RuntimeError("CDP connection closed")
            data.extend(chunk)
        return bytes(data)

    def send(self, payload: dict) -> None:
        raw = json.dumps(payload, separators=(",", ":")).encode()
        first = 0x81
        length = len(raw)
        mask_key = os.urandom(4)
        masked = bytes(byte ^ mask_key[i % 4] for i, byte in enumerate(raw))
        if length < 126:
            header = bytes((first, 0x80 | length))
        elif length < 65536:
            header = bytes((first, 0x80 | 126)) + struct.pack("!H", length)
        else:
            header = bytes((first, 0x80 | 127)) + struct.pack("!Q", length)
        self.sock.sendall(header + mask_key + masked)

    def receive(self) -> dict:
        first, second = self._read_exact(2)
        opcode = first & 0x0F
        length = second & 0x7F
        if length == 126:
            length = struct.unpack("!H", self._read_exact(2))[0]
        elif length == 127:
            length = struct.unpack("!Q", self._read_exact(8))[0]
        masked = second & 0x80
        mask_key = self._read_exact(4) if masked else b""
        body = self._read_exact(length)
        if masked:
            body = bytes(byte ^ mask_key[i % 4] for i, byte in enumerate(body))
        if opcode == 0x8:
            raise RuntimeError("CDP connection closed")
        if opcode == 0x9:
            self.sock.sendall(bytes((0x8A, len(body))) + body)
            return self.receive()
        if opcode != 0x1:
            return self.receive()
        return json.loads(body.decode())

    def call(self, method: str, params: dict | None = None) -> dict:
        self.next_id += 1
        call_id = self.next_id
        self.send({"id": call_id, "method": method, "params": params or {}})
        while True:
            message = self.receive()
            if message.get("id") == call_id:
                if "error" in message:
                    raise RuntimeError(f"{method}: {message['error']}")
                return message.get("result", {})

    def evaluate(self, expression: str):
        result = self.call(
            "Runtime.evaluate",
            {"expression": expression, "returnByValue": True, "awaitPromise": True},
        )
        exception = result.get("exceptionDetails")
        if exception:
            raise RuntimeError(f"browser expression failed: {exception}")
        return result.get("result", {}).get("value")

    def navigate(self, url: str) -> None:
        self.call("Page.navigate", {"url": url})
        time.sleep(0.25)

    def screenshot(self, path: Path) -> None:
        result = self.call("Page.captureScreenshot", {"format": "png", "fromSurface": True})
        path.write_bytes(base64.b64decode(result["data"]))

    def close(self) -> None:
        self.sock.close()


def browser_target(port: int) -> str:
    with urllib.request.urlopen(f"http://127.0.0.1:{port}/json/list", timeout=10) as response:
        targets = json.load(response)
    for target in targets:
        if target.get("type") == "page" and target.get("webSocketDebuggerUrl"):
            return target["webSocketDebuggerUrl"]
    raise RuntimeError("Chrome did not expose a page target")


def wait_for(browser: WebSocket, expression: str, timeout: float = 8) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if browser.evaluate(expression):
            return
        time.sleep(0.08)
    details = browser.evaluate("({href: location.href, title: document.title, body: document.body ? document.body.innerText.slice(0, 240) : '', ids: [...document.querySelectorAll('[id]')].map(e => e.id)})")
    raise TimeoutError(f"timed out waiting for: {expression}; page={details!r}")


def font(size: int, bold: bool = False) -> ImageFont.FreeTypeFont:
    name = "DejaVuSans-Bold.ttf" if bold else "DejaVuSans.ttf"
    return ImageFont.truetype(f"/usr/share/fonts/truetype/dejavu/{name}", size)


def mono_font(size: int) -> ImageFont.FreeTypeFont:
    return ImageFont.truetype("/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf", size)


def startup_frame(path: Path, log_path: Path) -> None:
    lines = log_path.read_text(encoding="utf-8", errors="replace").splitlines()
    while lines and not lines[0].strip():
        lines.pop(0)
    image = Image.new("RGB", (1200, 760), "#0d1117")
    draw = ImageDraw.Draw(image)
    draw.rounded_rectangle((46, 44, 1154, 716), radius=22, fill="#161b22", outline="#30363d", width=2)
    draw.rectangle((46, 44, 1154, 94), fill="#21262d")
    for x, color in ((77, "#ff5f57"), (101, "#febc2e"), (125, "#28c840")):
        draw.ellipse((x - 7, 63 - 7, x + 7, 63 + 7), fill=color)
    draw.text((170, 51), "ServeIt · actual startup console", fill="#8b949e", font=font(19, True))
    y = 108
    terminal_font = mono_font(11)
    for line in lines[:34]:
        if "█" in line or "▀" in line or "▄" in line:
            color = "#f0f6fc"
        elif line.startswith("http://") or line.startswith("192.168"):
            color = "#79c0ff"
        elif line.startswith("+") or line.startswith("|"):
            color = "#c9d1d9"
        else:
            color = "#8b949e"
        draw.text((68, y), line, fill=color, font=terminal_font)
        y += 17
    image.save(path)


def phone_frame(source: Path, destination: Path, title: str) -> None:
    screen = Image.open(source).convert("RGB")
    if screen.size != (375, 760):
        screen = screen.resize((375, 760), Image.Resampling.LANCZOS)
    image = Image.new("RGB", (1200, 760), "#111827")
    draw = ImageDraw.Draw(image)
    draw.text((70, 94), title, fill="#f9fafb", font=font(29, True))
    draw.text((70, 143), "Guest view · no account needed", fill="#9ca3af", font=font(19))
    x = 700
    draw.rounded_rectangle((x - 9, -9, x + 384, 769), radius=34, fill="#030712")
    image.paste(screen, (x, 0))
    draw.rounded_rectangle((x - 9, -9, x + 384, 769), radius=34, outline="#4b5563", width=3)
    image.save(destination)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--cdp-port", type=int, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--admin-password", default="adminpass")
    parser.add_argument("--startup-log", type=Path, required=True)
    args = parser.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)
    base_url = args.base_url.rstrip("/")
    startup_frame(args.out / "01-startup.png", args.startup_log)

    browser = WebSocket(browser_target(args.cdp_port))
    try:
        browser.call("Page.enable")
        browser.call("Runtime.enable")
        browser.call("Network.enable")
        browser.call(
            "Network.setExtraHTTPHeaders",
            {"headers": {"Authorization": "Basic " + base64.b64encode(f"admin:{args.admin_password}".encode()).decode()}},
        )
        browser.call("Network.setCookie", {"name": "serveit_theme", "value": "modern", "url": base_url + "/"})
        browser.call("Emulation.setDeviceMetricsOverride", {"width": 1200, "height": 760, "deviceScaleFactor": 1, "mobile": False})

        browser.navigate(base_url + "/admin")
        wait_for(browser, "!!document.querySelector('#shares')")
        browser.screenshot(args.out / "02-admin.png")

        browser.navigate(base_url + "/admin?path=Project")
        wait_for(browser, "document.querySelectorAll('#t .pick').length >= 6")
        browser.screenshot(args.out / "03-folder.png")

        browser.evaluate(
            """(() => {
                const wanted = new Set(['Project/brief.pdf','Project/budget.csv','Project/notes.txt','Project/Photos']);
                document.querySelectorAll('#t .pick').forEach(p => { if (wanted.has(p.value)) { p.click(); } });
                return true;
            })()"""
        )
        wait_for(browser, "document.querySelector('#dock').classList.contains('on') && document.querySelectorAll('#t .pick:checked').length === 4")
        browser.screenshot(args.out / "04-selected.png")

        browser.evaluate("document.querySelector('#go').click()")
        wait_for(browser, "document.querySelector('#qr').open")
        browser.screenshot(args.out / "05-qr.png")
        share_url = browser.evaluate("document.querySelector('#qu').textContent")
        if not share_url or "/s/" not in share_url:
            raise RuntimeError(f"could not read generated share URL: {share_url!r}")
        token = share_url.rstrip("/").split("/s/", 1)[1].split("/", 1)[0]

        browser.call("Emulation.setDeviceMetricsOverride", {"width": 375, "height": 760, "deviceScaleFactor": 1, "mobile": True})
        browser.navigate(f"{base_url}/s/{token}/")
        wait_for(browser, "document.querySelectorAll('#t .pick').length >= 4")
        browser.screenshot(args.out / "06-phone-raw.png")
        phone_frame(args.out / "06-phone-raw.png", args.out / "06-phone.png", "Scan QR → guest link")

        browser.evaluate(
            """(() => {
                const wanted = new Set(['Project/Photos','Project/brief.pdf','Project/budget.csv']);
                document.querySelectorAll('#t .pick').forEach(p => { if (wanted.has(p.value)) { p.click(); } });
                return true;
            })()"""
        )
        wait_for(browser, "document.querySelector('#dock').classList.contains('on') && document.querySelectorAll('#t .pick:checked').length === 3")
        browser.screenshot(args.out / "07-phone-selected-raw.png")
        phone_frame(args.out / "07-phone-selected-raw.png", args.out / "07-phone-selected.png", "Select only what you need")

        browser.evaluate("document.querySelector('#go').click()")
        time.sleep(0.8)
        browser.screenshot(args.out / "08-phone-download-raw.png")
        phone_frame(args.out / "08-phone-download-raw.png", args.out / "08-phone-download.png", "Download selected files")
    finally:
        browser.close()


if __name__ == "__main__":
    main()
