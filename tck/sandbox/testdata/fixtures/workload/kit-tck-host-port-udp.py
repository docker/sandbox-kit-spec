#!/usr/bin/env python3
"""Report UDP observations; only the suite knows the host's response key."""
import base64
import concurrent.futures
import json
import secrets
import socket
import sys
import threading


def observe(port, mode):
    barrier = threading.Barrier(4) if mode == 'clients' else None

    def client(index):
        sizes = [32] if mode == 'single' else [31, 0, 257, 1024, 17]
        records = []
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
            sock.bind(('127.0.0.1', 0))
            sock.settimeout(2)
            for size in sizes:
                data = secrets.token_bytes(size)
                if barrier:
                    barrier.wait(timeout=5)
                sock.sendto(data, ('127.0.0.1', port))
                reply, peer = sock.recvfrom(65535)
                if peer != ('127.0.0.1', port):
                    raise ValueError('reply did not originate at the mapped endpoint')
                records.append({'sent': base64.b64encode(data).decode(),
                                'received': base64.b64encode(reply).decode()})
        return records

    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        return list(pool.map(client, range(4 if barrier else 1)))


if __name__ == '__main__':
    try:
        print(json.dumps(observe(int(sys.argv[1]), sys.argv[2])))
    except OSError as error:
        print(str(error), file=sys.stderr)
        sys.exit(7)
