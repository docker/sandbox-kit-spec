#!/usr/local/bin/python3
"""A stand-in engine: it records what the runtime gave it, then answers pings.

A fresh boot id on every start, published once the socket is bound, lets
the suite tell an engine answering this boot from one started before it.
The token kept in /var/lib/docker lets it tell retained storage from a new
or shared store. Records and the token are readable by the image user,
because the suite reads them as that user. The socket is left root's
and 0600, as a real engine without a group leaves it, so making it usable
by the image user is the runtime's job. Flags a runtime passes are
recorded, so the suite can judge them against the image's daemon.json,
and otherwise ignored.
"""

import http.server
import json
import os
import socketserver
import sys
import uuid

# A runtime's umask would otherwise decide whether the image user can
# read the records.
os.umask(0o022)

RECORDS = "/var/tmp/kit-tck-docker"
STORE = "/var/lib/docker/kit-tck-storage"
SOCKET = "/var/run/docker.sock"


def publish(name, value):
    path = os.path.join(RECORDS, name)
    with open(path + ".tmp", "w") as f:
        f.write(value + "\n")
    os.chmod(path + ".tmp", 0o644)
    os.replace(path + ".tmp", path)


class Ping(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"OK"
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    do_HEAD = do_GET

    def log_message(self, *args):
        pass


class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


os.makedirs(RECORDS, mode=0o755, exist_ok=True)
publish("argv", json.dumps(sys.argv[1:]))
os.makedirs(os.path.dirname(STORE), exist_ok=True)
os.chmod(os.path.dirname(STORE), 0o711)
if not os.path.exists(STORE):
    with open(STORE + ".tmp", "w") as f:
        f.write(uuid.uuid4().hex + "\n")
    os.chmod(STORE + ".tmp", 0o644)
    os.replace(STORE + ".tmp", STORE)

if os.path.exists(SOCKET):
    os.unlink(SOCKET)
with Server(SOCKET, Ping) as server:
    os.chmod(SOCKET, 0o600)
    publish("boot", uuid.uuid4().hex)
    server.serve_forever()
