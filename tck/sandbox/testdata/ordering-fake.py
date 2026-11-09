"""Execute the API-produced plan against isolated files and a real ping service."""
import contextlib
import http.client
import json
import os
import pathlib
import shutil
import socket
import subprocess
import sys
import time
import uuid
import tempfile

state, claims, broken, verb, *args = sys.argv[1:]
base = pathlib.Path(state) / 'ordering'
base.mkdir(exist_ok=True)
fixtures = pathlib.Path(__file__).parent / 'fixtures'


def remap(value, root):
    value = value.replace('/var/run', str(pathlib.Path('/tmp') / ('kit-order-' + root.parent.name[:12])))
    for prefix in ('/var/tmp', '/etc/docker', '/var/lib/docker'):
        value = value.replace(prefix, str(root) + prefix)
    return value


def load_plan(kits, rejected, overrides):
    paths = []
    for kit in kits:
        path = pathlib.Path(kit)
        if not path.is_dir():
            path = fixtures / path.name.split(':', 1)[0]
        paths.append(str(path))
    result = subprocess.run([os.environ['KIT_TCK_FAKE_ORDERING']], input=json.dumps({
        'Kits': paths, 'Claims': claims.split(','), 'Rejected': rejected, 'Args': overrides,
    }), text=True, capture_output=True, check=False)
    if result.returncode:
        if broken == 'ordering-applies-before-refusal' and overrides.get('auto_start') == 'true':
            with tempfile.TemporaryDirectory(dir=base) as directory:
                control = load_plan(kits, rejected, overrides | {'auto_start': 'false'})
                boot({'plan': control, 'once': [], 'boots': 0}, pathlib.Path(directory) / 'root')
        if broken not in ('ordering-accepts-contradiction', 'ordering-accepts-order-conflict'):
            print(result.stderr, file=sys.stderr, end='')
            sys.exit(2)
        return []
    return json.loads(result.stdout)


def run_hooks(hooks, root, seen, bindings):
    for hook in hooks:
        command = hook['command']
        signature = json.dumps(command)
        if broken == 'ordering-deduplicates-hooks' and signature in seen:
            continue
        seen.add(signature)
        if isinstance(command, str):
            argv = ['sh', '-c', remap(command, root)]
        else:
            argv = [remap(word, root) for word in command]
        for i, word in enumerate(argv):
            for guest, host in bindings.items():
                word = word.replace('http://localhost:' + str(guest) + '/', 'http://127.0.0.1:' + str(host) + '/')
            argv[i] = word
        result = subprocess.run(argv, text=True, capture_output=True, check=False,
                                env={'PATH': os.environ['PATH'], 'HOME': str(root)})
        if result.returncode:
            print(result.stderr, file=sys.stderr, end='')
            sys.exit(1)


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__('localhost', timeout=1)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(str(self.path))


def boot(data, root):
    socket_root = pathlib.Path(remap('/var/run', root))
    socket_root.mkdir(exist_ok=True)
    for path in ('var/tmp', 'etc/docker', 'var/lib/docker'):
        (root / path).mkdir(parents=True, exist_ok=True)
    if broken == 'ordering-applies-skipped':
        (root / 'var/tmp/ordering-skipped').write_text('skipped')
    steps = list(data['plan'])
    if broken in ('ordering-ignores-order', 'ordering-reverses-provider-hooks'):
        steps = [step for stage in ('configure', 'setup', 'install', 'start')
                 for step in reversed(steps) if step['stage'] == stage]
    if broken == 'ordering-unstable-ties':
        for i in range(len(steps)):
            j = i + 1
            while j < len(steps) and (steps[j]['stage'], steps[j]['order']) == (steps[i]['stage'], steps[i]['order']):
                j += 1
            steps[i:j] = reversed(steps[i:j])
    if broken == 'ordering-batches-files':
        steps.sort(key=lambda step: step['action'] == 'files')
    seen = set()
    bindings = {}
    entries = {}
    engine = None
    try:
        for step in steps:
            action = step['action']
            stage = step['stage']
            typ = step['capability']['type']
            config = step['capability'].get('config', {})
            identity = step['entryID'] + '/' + stage + '/' + action
            if broken == 'ordering-first-entry-only':
                entries.setdefault((typ, stage), step['entryID'])
                if entries[(typ, stage)] != step['entryID']:
                    continue
            if stage == 'install' and identity in data['once'] and broken != 'ordering-reinstalls':
                continue
            if typ.endswith('/host-port@1') and action == 'bind':
                if broken != 'ordering-no-trace-mapping':
                    bindings[config['container']] = config['host']
            elif typ.endswith('/lifecycle@1'):
                if action == 'files':
                    if data['boots'] and broken == 'ordering-files-once':
                        continue
                    for entry in config.get('files', []):
                        destination = pathlib.Path(remap(entry['path'], root))
                        destination.parent.mkdir(parents=True, exist_ok=True)
                        if destination.exists() and not entry.get('overwrite', True):
                            continue
                        destination.write_text(entry['content'])
                elif action == 'install':
                    run_hooks(config.get('install', []), root, seen, bindings)
                elif action == 'startup':
                    if broken == 'ordering-runs-whole-entry':
                        run_hooks(config.get('install', []), root, seen, bindings)
                    if data['boots'] and broken == 'ordering-startup-once':
                        continue
                    run_hooks(config.get('startup', []), root, seen, bindings)
            elif typ.endswith('/dockerd@1') and stage == 'start':
                if broken == 'ordering-service-not-ready':
                    continue
                source = (fixtures / 'ordering-workload/dockerd.py').read_text()
                engine = subprocess.Popen([sys.executable, '-c', remap(source, root)],
                                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                deadline = time.monotonic() + 5
                while True:
                    try:
                        with contextlib.closing(UnixHTTP(pathlib.Path(remap('/var/run/docker.sock', root)))) as connection:
                            connection.request('GET', '/_ping')
                            if connection.getresponse().status == 200:
                                break
                    except OSError:
                        pass
                    if engine.poll() is not None or time.monotonic() > deadline:
                        print('engine did not answer within 5 seconds', file=sys.stderr)
                        sys.exit(1)
                    time.sleep(0.01)
            if stage == 'install' and identity not in data['once']:
                data['once'].append(identity)
        data['boots'] += 1
    finally:
        if engine is not None:
            engine.terminate()
            engine.wait(timeout=5)
        shutil.rmtree(socket_root)


if verb == 'create':
    kits, rejected, overrides = [], [], {}
    i = 0
    while i < len(args):
        if args[i].startswith('--'):
            if args[i] == '--arg':
                key, value = args[i + 1].split('=', 1)
                overrides[key] = value
            if args[i] == '--reject-capability':
                rejected.append(args[i + 1])
            i += 2
        else:
            kits.append(args[i])
            i += 1
    plan = load_plan(kits, rejected, overrides)
    identifier = uuid.uuid4().hex
    directory = base / identifier
    directory.mkdir()
    root = directory / 'root'
    data = {'plan': plan, 'once': [], 'boots': 0, 'kits': kits, 'rejected': rejected, 'overrides': overrides}
    boot(data, root)
    (directory / 'state.json').write_text(json.dumps(data))
    print(identifier)
else:
    identifier, *args = args
    directory = base / identifier
    root = directory / 'root'
    data = json.loads((directory / 'state.json').read_text())
    if verb == 'exec':
        if args and args[0] == '--':
            args = args[1:]
        result = subprocess.run([remap(word, root) for word in args], capture_output=True, check=False)
        sys.stdout.buffer.write(result.stdout)
        sys.stderr.buffer.write(result.stderr)
        sys.exit(result.returncode)
    elif verb == 'start':
        boot(data, root)
    elif verb == 'recreate':
        shutil.rmtree(root)
        data['plan'] = load_plan(data['kits'], data['rejected'], data['overrides'])
        boot(data, root)
    elif verb == 'rm':
        shutil.rmtree(directory)
        sys.exit(0)
    elif verb != 'stop':
        sys.exit(1)
    (directory / 'state.json').write_text(json.dumps(data))
