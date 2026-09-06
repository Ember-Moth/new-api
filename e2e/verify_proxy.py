"""Verify the repository Nginx routing, SSE flushing, and WebSocket tunnel.

Run: python3 e2e/verify_proxy.py
Requires nginx on PATH. Uses only ephemeral loopback backends and a temporary
Nginx prefix; does not start or change a system service.
"""
import base64
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def main():
    nginx = shutil.which("nginx")
    if not nginx:
        raise SystemExit("nginx is required on PATH")
    release_stream = threading.Event()

    class Backend(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *args):
            pass

        def do_GET(self):
            if self.path == "/v1/stream":
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.write(b"data: first\n\n")
                self.wfile.flush()
                if not release_stream.wait(10):
                    return
                self.wfile.write(b"data: second\n\n")
                self.wfile.flush()
                self.close_connection = True
                return
            if self.path == "/v1/realtime":
                assert self.headers.get("Upgrade", "").lower() == "websocket"
                assert self.headers.get("Connection", "").lower() == "upgrade"
                key = self.headers["Sec-WebSocket-Key"]
                accept = base64.b64encode(hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
                self.send_response(101)
                self.send_header("Upgrade", "websocket")
                self.send_header("Connection", "Upgrade")
                self.send_header("Sec-WebSocket-Accept", accept)
                self.end_headers()
                header = self.rfile.read(2)
                assert header == b"\x81\x84", header
                mask = self.rfile.read(4)
                payload = self.rfile.read(4)
                assert bytes(c ^ mask[i % 4] for i, c in enumerate(payload)) == b"ping"
                self.wfile.write(b"\x81\x04pong")
                self.wfile.flush()
                self.close_connection = True
                return
            data = json.dumps({"plane": self.server.plane, "path": self.path}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    backends = []
    process = None
    with tempfile.TemporaryDirectory(prefix="new-api-proxy-") as directory:
        root = Path(directory)
        (root / "logs").mkdir()
        try:
            for plane in ("control", "data"):
                server = ThreadingHTTPServer(("127.0.0.1", 0), Backend)
                server.plane = plane
                threading.Thread(target=server.serve_forever, daemon=True).start()
                backends.append(server)
            with socket.socket() as reserve:
                reserve.bind(("127.0.0.1", 0))
                port = reserve.getsockname()[1]
            config = (Path(__file__).resolve().parents[1] / "deploy/nginx.conf").read_text()
            config = config.replace("new-api:3000", f"127.0.0.1:{backends[0].server_port}")
            config = config.replace("new-api-data:3000", f"127.0.0.1:{backends[1].server_port}")
            config = config.replace("listen 3000;", f"listen 127.0.0.1:{port};")
            config_path = root / "nginx.conf"
            config_path.write_text(config)
            subprocess.run([nginx, "-t", "-p", directory + os.sep, "-c", str(config_path)], check=True)
            process = subprocess.Popen([nginx, "-p", directory + os.sep, "-c", str(config_path), "-g", "daemon off;"], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            base = f"http://127.0.0.1:{port}"
            deadline = time.monotonic() + 5
            while True:
                if process.poll() is not None:
                    raise RuntimeError(process.stderr.read().decode())
                try:
                    with opener.open(base + "/healthz", timeout=.2):
                        break
                except OSError:
                    if time.monotonic() >= deadline:
                        raise
                    time.sleep(.02)
            for path, expected in {
                "/api/user/self": "control", "/console": "control", "/dashboard/billing/usage": "control",
                "/v1/dashboard/billing/usage": "control", "/v1/models": "data", "/v1beta/models": "data",
                "/pg/chat/completions": "data", "/mj/task/test/fetch": "data", "/fast/mj/task/test/fetch": "data",
            }.items():
                with opener.open(base + path, timeout=3) as response:
                    assert json.load(response)["plane"] == expected, path
            with opener.open(base + "/v1/stream", timeout=3) as response:
                assert response.readline() == b"data: first\n"
                assert response.readline() == b"\n"
                release_stream.set()
                assert response.read() == b"data: second\n\n"
            with socket.create_connection(("127.0.0.1", port), timeout=3) as connection:
                connection.sendall(b"GET /v1/realtime HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
                stream = connection.makefile("rb")
                assert b"101" in stream.readline()
                headers = {}
                while True:
                    line = stream.readline()
                    if line == b"\r\n":
                        break
                    assert line, "WebSocket handshake ended before headers completed"
                    name, value = line.decode().split(":", 1)
                    headers[name.lower()] = value.strip()
                assert headers["sec-websocket-accept"] == "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
                mask = b"abcd"
                connection.sendall(b"\x81\x84" + mask + bytes(c ^ mask[i % 4] for i, c in enumerate(b"ping")))
                assert stream.read(6) == b"\x81\x04pong"
            subprocess.run([nginx, "-v"], check=True)
            print("Repository Nginx configuration: routing, unbuffered SSE and WebSocket tunnel passed.")
        finally:
            release_stream.set()
            if process is not None and process.poll() is None:
                process.terminate()
                process.wait(timeout=5)
            for server in backends:
                server.shutdown()
                server.server_close()


if __name__ == "__main__":
    main()
