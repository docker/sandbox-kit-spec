#!/usr/local/bin/python3
"""Read engine configuration at launch and delay until ping readiness."""
import http.server
import os
import pathlib
import signal
import socketserver
import sys
import time

ROOT = pathlib.Path('/var/tmp')
config = pathlib.Path('/etc/docker/daemon.json').read_text()
if 'from-lifecycle' in config:
    if (ROOT / 'ordering-engine-file').read_text() != 'placed':
        sys.exit('Start/Place did not finish before engine launch')
(ROOT / 'ordering-engine-config').write_text(config)
ready_at = time.monotonic() + float(os.environ.get('KIT_TCK_READINESS_DELAY', '0.03'))


class Ping(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200 if time.monotonic() >= ready_at else 503)
        self.end_headers()
        self.wfile.write(b'OK,')

    def log_message(self, *_args):
        pass


def stop_engine(_sig, _frame):
    with (ROOT / 'ordering-stop').open('a') as record:
        record.write('engine,')
    sys.exit(0)


signal.signal(signal.SIGTERM, stop_engine)
socket = '/var/run/docker.sock'
if os.path.exists(socket):
    os.unlink(socket)
with socketserver.UnixStreamServer(socket, Ping) as server:
    os.chown(socket, int(os.environ.get('KIT_TCK_PROBE_UID', '1000')), -1)
    os.chmod(socket, 0o600)
    server.serve_forever()
