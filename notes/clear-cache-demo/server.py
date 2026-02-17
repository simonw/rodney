#!/usr/bin/env python3
"""Tiny HTTP server that serves a page with aggressive caching headers.

The /data endpoint returns a random number with a long Cache-Control max-age,
so a normal reload will keep showing the same value while a force reload
(Shift+Refresh / rodney reload --hard) will fetch a fresh one.
"""
import http.server
import random
import datetime

PORT = 9876

HTML = """\
<!DOCTYPE html>
<html>
<head><title>Cache Demo</title></head>
<body>
  <h1>Cache Demo</h1>
  <p>Loaded at: <span id="time">{time}</span></p>
  <p>Random value: <span id="value">{value}</span></p>
</body>
</html>
"""


class CachingHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        now = datetime.datetime.now().strftime("%H:%M:%S")
        value = random.randint(1000, 9999)

        body = HTML.format(time=now, value=value).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html")
        self.send_header("Content-Length", str(len(body)))
        # Aggressive caching: tell the browser this is good for 1 hour
        self.send_header("Cache-Control", "public, max-age=3600")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        pass  # suppress request logging


if __name__ == "__main__":
    with http.server.HTTPServer(("127.0.0.1", PORT), CachingHandler) as srv:
        print(f"Cache demo server on http://127.0.0.1:{PORT}")
        srv.serve_forever()
