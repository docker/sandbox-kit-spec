#!/usr/bin/env python3
"""Report a framed binary exchange; only the host knows the response key."""
import base64
import json
import secrets
import socket
import struct
import sys
import time


def observe(port):
    data = bytes(range(256)) + secrets.token_bytes(4096 - 256)
    deadline = time.monotonic() + 5
    with socket.create_connection(('127.0.0.1', port), timeout=5) as sock:
        def read_full(size):
            result = bytearray()
            while len(result) < size:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise TimeoutError('TCP exchange timed out')
                sock.settimeout(remaining)
                chunk = sock.recv(size - len(result))
                if not chunk:
                    raise OSError('truncated TCP response')
                result.extend(chunk)
            return bytes(result)

        sock.sendall(struct.pack('!I', len(data)) + data)
        size, = struct.unpack('!I', read_full(4))
        if size != len(data) + 32:
            raise OSError('unexpected TCP response length')
        reply = read_full(size)
    return {'sent': base64.b64encode(data).decode(),
            'received': base64.b64encode(reply).decode()}


if __name__ == '__main__':
    try:
        print(json.dumps(observe(int(sys.argv[1]))))
    except OSError as error:
        print(str(error), file=sys.stderr)
        sys.exit(7)
