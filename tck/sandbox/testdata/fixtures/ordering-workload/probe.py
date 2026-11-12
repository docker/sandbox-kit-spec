#!/usr/local/bin/python3
"""Observe executed hooks and shutdown while the supporting engine answers."""
import contextlib
import http.client
import os
import pathlib
import signal
import socket
import sys
import time

ROOT = pathlib.Path('/var/tmp')


class Engine(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(2)
        self.sock.connect('/var/run/docker.sock')


def append(name, value):
    with (ROOT / name).open('a') as target:
        target.write(value)


def snapshot(name):
    path = ROOT / name
    return path.read_text() if path.exists() else '<missing>'


mode = sys.argv[1]
if mode == 'workload':
    append('ordering-entrypoint', '|'.join(snapshot(name) for name in (
        'ordering-install', 'ordering-startup', 'ordering-file')) + '\n')

    def stop_workload(_sig, _frame):
        append('ordering-stop', 'workload,')
        sys.exit(0)

    signal.signal(signal.SIGTERM, stop_workload)
elif mode == 'background':
    name = sys.argv[2]

    def stop_program(_sig, _frame):
        with contextlib.closing(Engine('localhost')) as engine:
            engine.request('GET', '/_ping')
            response = engine.getresponse()
            ready = response.status == 200 and response.read() == b'OK,'
        placed = (ROOT / 'ordering-stop-resource').read_text() == 'placed'
        append('ordering-stop', name + (':ready,' if ready and placed else ':missing,'))
        (ROOT / ('ordering-running-' + name)).unlink()
        sys.exit(0)

    signal.signal(signal.SIGTERM, stop_program)
    (ROOT / ('ordering-running-' + name)).write_text(str(os.getpid()))
elif mode == 'wait':
    deadline = time.monotonic() + 5
    while not (ROOT / ('ordering-running-' + sys.argv[2])).exists():
        if time.monotonic() > deadline:
            sys.exit('background program did not start')
        time.sleep(0.01)
    sys.exit(0)
else:
    sys.exit('unknown observation')

while True:
    signal.pause()
