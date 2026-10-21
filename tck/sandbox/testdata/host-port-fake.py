"""Model endpoint selection, but obtain response bytes from the real host service."""
import base64
import importlib.util
import socket
import types
import json
import pathlib
import sys
import urllib.error
import urllib.request
import uuid

state, claims, broken, verb, *args = sys.argv[1:]
root = pathlib.Path(state) / 'host-port'
root.mkdir(exist_ok=True)
cap = 'com.docker.sandbox/host-port@1'


def refuse():
    sys.exit(2)


def request(port):
    try:
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open('http://127.0.0.1:' + str(port) + '/', timeout=2) as response:
            return response.read().decode()
    except (OSError, urllib.error.URLError):
        sys.exit(7)


def request_tcp(port):
    sys.dont_write_bytecode = True
    spec = importlib.util.spec_from_file_location('tcp_probe', pathlib.Path(__file__).parent / 'fixtures/workload/kit-tck-host-port-tcp.py')
    probe = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(probe)

    class Endpoint:
        def __enter__(self):
            self.sock = socket.create_connection(('127.0.0.1', int(port)), timeout=5)
            return self

        def __exit__(self, *args):
            self.sock.close()

        def settimeout(self, timeout):
            self.sock.settimeout(timeout)

        def sendall(self, data):
            if broken == 'host-port-tcp-corrupts-request':
                data = data[:-1] + bytes([data[-1] ^ 1])
            return self.sock.sendall(data)

        def recv(self, size):
            data = self.sock.recv(size)
            if broken == 'host-port-tcp-corrupts-reply' and data:
                data = data[:-1] + bytes([data[-1] ^ 1])
            return data

    probe.socket = types.SimpleNamespace(create_connection=lambda *args, **kwargs: Endpoint())
    try:
        return json.dumps(probe.observe(int(port)))
    except OSError:
        sys.exit(7)


def request_udp(port, mode, destination=None):
    if destination is None:
        destination = port
    sys.dont_write_bytecode = True
    spec = importlib.util.spec_from_file_location('udp_probe', pathlib.Path(__file__).parent / 'fixtures/workload/kit-tck-host-port-udp.py')
    probe = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(probe)

    class Endpoint:
        def __init__(self, *args):
            self.sock = socket.socket(*args)

        def __enter__(self):
            return self

        def __exit__(self, *args):
            self.sock.close()

        def bind(self, address):
            self.sock.bind(address)

        def settimeout(self, timeout):
            self.sock.settimeout(timeout)

        def sendto(self, data, address):
            if broken == 'host-port-udp-no-forward' or (broken == 'host-port-udp-drops-empty' and not data):
                raise socket.timeout('UDP datagram dropped')
            if broken == 'host-port-udp-corrupts-request':
                data = bytes(b ^ 1 for b in data)
            return self.sock.sendto(data, ('127.0.0.1', int(destination)))

        def recvfrom(self, size):
            data, _ = self.sock.recvfrom(size)
            if broken == 'host-port-udp-corrupts-reply':
                data = bytes(b ^ 1 for b in data)
            if broken == 'host-port-udp-splits-datagrams' and len(data) > 1:
                data = data[:len(data)//2]
            return data, ('127.0.0.1', int(port))

    probe.socket = types.SimpleNamespace(socket=Endpoint, AF_INET=socket.AF_INET, SOCK_DGRAM=socket.SOCK_DGRAM)
    try:
        records = probe.observe(int(port), mode)
        if broken == 'host-port-udp-mixes-clients' and mode == 'clients':
            # Route each host reply to the next guest client, not its origin.
            replies = [[r['received'] for r in client] for client in records]
            for index, client in enumerate(records):
                for offset, record in enumerate(client):
                    record['received'] = replies[(index + 1) % len(records)][offset]
        return json.dumps(records)
    except OSError:
        sys.exit(7)


if verb == 'create':
    values = {'host_port': '18431', 'container_port': '18432'}
    kits = []
    rejected = []
    overrides = set()
    i = 0
    while i < len(args):
        arg = args[i]
        if arg.startswith('--'):
            value = args[i + 1]
            if arg == '--arg':
                key, value = value.split('=', 1)
                values[key] = value
                overrides.add(key)
            elif arg == '--reject-capability':
                rejected.append(value)
            i += 2
        else:
            kits.append(pathlib.Path(arg).name)
            i += 1
    declared = {
        'host-port-udp': {'host_port', 'container_port'},
        'host-port-udp-hooks': {'host_port', 'container_port'},
        'host-port-udp-default': {'host_port', 'default_host_port'},
        'host-port-udp-alias': {'host_port', 'other_host_port', 'tcp_host_port'},
        'host-port': {'host_port', 'container_port'},
        'host-port-hooks': {'host_port', 'container_port'},
        'host-port-default': {'host_port', 'default_host_port'},
        'host-port-alias': {'host_port', 'other_host_port'},
    }
    allowed = set().union(*(declared.get(kit, set()) for kit in kits))
    if overrides - allowed:
        print('undeclared argument override', file=sys.stderr)
        sys.exit(1)
    if cap not in claims.split(',') or (cap in rejected and broken not in ('host-port-ignores-rejection', 'host-port-udp-ignores-rejection')):
        refuse()
    host = int(values['host_port'])
    container = int(values['container_port'])
    mappings = {}
    for kit in kits:
        transport = 'udp' if kit.startswith('host-port-udp') else 'tcp'
        kit = kit.replace('host-port-udp', 'host-port')
        entries = []
        if kit in ('host-port', 'host-port-hooks'):
            entries = [(container, host)]
        elif kit == 'host-port-default':
            default = int(values.get('default_host_port', host))
            entries = [(default, default)]
        elif kit == 'host-port-alias':
            entries = [(18433, host), (18434, int(values['other_host_port']))]
        for guest, destination in entries:
            key = str(guest) + ('/udp' if transport == 'udp' else '')
            if key in mappings:
                if broken in ('host-port-rejects-duplicate', 'host-port-udp-rejects-duplicate'):
                    refuse()
                if mappings[key] != destination and broken not in ('host-port-accepts-conflict', 'host-port-udp-accepts-conflict'):
                    refuse()
            mappings[key] = destination
    if 'host-port-udp-alias' in kits:
        if broken == 'host-port-udp-conflicts-with-tcp':
            refuse()
        mappings[str(host)] = int(values['tcp_host_port'])
    hooks = {}
    if 'host-port-hooks' in kits:
        for phase in ('install', 'startup'):
            hooks[phase] = '' if broken == 'host-port-late-' + phase else request(host)
    if 'host-port-udp-hooks' in kits:
        for phase in ('install', 'startup'):
            hooks['udp-' + phase] = '' if broken == 'host-port-udp-late-' + phase else request_udp(container, 'single', host)
    if broken == 'host-port-udp-probes-admission':
        request_udp(container, 'single', host)
    if broken == 'host-port-probes-admission':
        request(host)
    identity = 'hp-' + uuid.uuid4().hex
    (root / (identity + '.json')).write_text(json.dumps({'mappings': mappings, 'hooks': hooks}))
    print(identity)
    sys.exit(0)

file = root / (args[0] + '.json')
model = json.loads(file.read_text()) if file.exists() else {'mappings': {}, 'hooks': {}}
if verb == 'rm':
    file.unlink(missing_ok=True)
elif verb == 'selection':
    entries = sorted(str(host) + '->' + (guest if '/' in guest else guest + '/tcp') for guest, host in model['mappings'].items())
    if broken == 'host-port-udp-wrong-surface':
        entries = [entry.replace('/udp', '/tcp') for entry in entries]
    surface = {'hostPorts': entries}
    if broken == 'host-port-wrong-surface':
        surface = {'ports': entries}
    print(json.dumps({'selection': {}, 'surface': surface}))
elif verb == 'exec':
    command = args[2:]
    if command[0] == 'cat':
        phase = command[1].rsplit('-', 1)[-1]
        if 'host-port-udp-' in command[1]:
            phase = 'udp-' + phase
        print(model['hooks'][phase], end='')
    elif command[0] == 'kit-tck-host-port-udp':
        port, mode = command[1:]
        destination = model['mappings'].get(port + '/udp')
        if destination is None:
            if broken == 'host-port-udp-unrelated-listener':
                print('unrelated local UDP response', end='')
                sys.exit(0)
            if broken in ('host-port-udp-outbound-only', 'host-port-udp-active-outbound-only') and (broken == 'host-port-udp-outbound-only' or model['mappings']):
                request_udp(port, mode, int(port))
                sys.exit(7)
            if broken == 'host-port-udp-ungranted-access' or (broken == 'host-port-udp-active-ungranted-access' and model['mappings']):
                destination = int(port)
            else:
                sys.exit(7)
        print(request_udp(port, mode, destination), end='')
    elif command[0] == 'kit-tck-host-port-tcp':
        destination = model['mappings'].get(command[1])
        if destination is None or broken == 'host-port-tcp-no-forward':
            sys.exit(7)
        print(request_tcp(destination), end='')
    elif command[0] == 'curl':
        port = command[-1].split(':')[-1].split('/')[0]
        destination = model['mappings'].get(port)
        if destination is None:
            if broken == 'host-port-unrelated-listener':
                print('unrelated sandbox-local service', end='')
                sys.exit(0)
            if broken == 'host-port-denial-response':
                print('HTTP access denied', end='')
                sys.exit(0)
            if broken in ('host-port-outbound-only', 'host-port-active-outbound-only') and (broken == 'host-port-outbound-only' or model['mappings']):
                request(int(port))
                sys.exit(7)
            if broken == 'host-port-ungranted-access' or (broken == 'host-port-active-ungranted-access' and model['mappings']):
                destination = int(port)
            else:
                sys.exit(7)
        if broken == 'host-port-no-forward':
            sys.exit(7)
        print(request(destination), end='')
    else:
        sys.exit(2)
else:
    sys.exit(2)
