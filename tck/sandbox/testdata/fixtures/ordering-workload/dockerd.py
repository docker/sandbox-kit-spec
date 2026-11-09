#!/usr/local/bin/python3
"""Record the configuration consumed at engine start, then answer pings."""
import http.server
import os
import pwd
import socketserver

os.umask(0o022)

with open('/etc/docker/daemon.json') as source:
    config = source.read()
with open('/var/tmp/ordering-engine-config', 'w') as record:
    record.write(config)


class Ping(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b'OK,')

    def log_message(self, *args):
        pass


socket = '/var/run/docker.sock'
if os.path.exists(socket):
    os.unlink(socket)
with socketserver.UnixStreamServer(socket, Ping) as server:
    agent = pwd.getpwnam('agent')
    os.chown(socket, agent.pw_uid, agent.pw_gid)
    os.chmod(socket, 0o600)
    server.serve_forever()
