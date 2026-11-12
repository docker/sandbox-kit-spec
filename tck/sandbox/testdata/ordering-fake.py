"""Execute the reference plan against real commands, Git clones and services."""
import contextlib
import http.client
import json
import os
import pathlib
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import uuid

state, claims, broken, verb, *args = sys.argv[1:]
base = pathlib.Path(state) / 'ordering'
base.mkdir(exist_ok=True)
fixtures = pathlib.Path(__file__).parent / 'fixtures'


def remap(value, root):
    value = value.replace('/var/run', str(pathlib.Path('/tmp') / ('kit-order-' + root.parent.name[:12])))
    for prefix in ('/var/tmp', '/etc/docker', '/var/lib/docker', '/home/agent/workspace', '/usr/local/bin/kit-tck-ordering'):
        value = value.replace(prefix, str(root) + prefix)
    return value


def load_plan(kits, rejected, overrides, workspace):
    paths = []
    for kit in kits:
        path = pathlib.Path(kit)
        if not path.is_dir():
            path = fixtures / path.name.split(':', 1)[0]
        paths.append(str(path))
    result = subprocess.run([os.environ['KIT_TCK_FAKE_ORDERING']], input=json.dumps({
        'Kits': paths, 'Claims': claims.split(','), 'Rejected': rejected,
        'Args': overrides, 'WorkspaceAssigned': bool(workspace), 'Broken': broken,
    }), text=True, capture_output=True, check=False)
    if result.returncode:
        premature = (broken == 'ordering-effects-before-cycle' and any(pathlib.Path(kit).name == 'ordering-cycle' for kit in kits) or
                     broken == 'ordering-effects-before-selection' and rejected or
                     broken == 'ordering-effects-before-conflict' and any(pathlib.Path(kit).name == 'ordering-conflict' for kit in kits))
        if premature:
            with tempfile.TemporaryDirectory(dir=base) as directory:
                control_kits = [kit for kit in kits if pathlib.Path(kit).name not in ('ordering-cycle', 'ordering-conflict')]
                plan = load_plan(control_kits, [], overrides, workspace)
                data = new_state(plan, workspace)
                root = pathlib.Path(directory) / 'root'
                try:
                    boot(data, root)
                finally:
                    stop(data)
                    shutil.rmtree(remap('/var/run', root), ignore_errors=True)
        if broken == 'ordering-accepts-cycle':
            return {'phases': [], 'stop': []}
        print(result.stderr, file=sys.stderr, end='')
        sys.exit(2)
    return json.loads(result.stdout)


def new_state(plan, workspace):
    return {'plan': plan, 'workspace': workspace, 'once': [], 'boots': 0, 'processes': []}


def wait_for(predicate, message):
    deadline = time.monotonic() + 5
    while not predicate():
        if time.monotonic() > deadline:
            raise RuntimeError(message)
        time.sleep(0.01)


def running(pid):
    try:
        return pathlib.Path('/proc', str(pid), 'stat').read_text().split()[2] != 'Z'
    except FileNotFoundError:
        return False


def stop(data):
    actions = list(data['plan'].get('stop') or [])
    if broken == 'ordering-forward-stop':
        actions.reverse()
    for action in actions:
        matches = [p for p in data['processes'] if p['entry'] == action['entryId'] and p['fragment'] == action['fragmentId']]
        for process in reversed(matches):
            terminate(process['pid'])
            data['processes'].remove(process)
    for process in reversed(data['processes']):
        terminate(process['pid'])
    data['processes'] = []


def terminate(pid):
    if running(pid):
        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            return
        try:
            wait_for(lambda: not running(pid), 'program did not stop')
        except RuntimeError:
            try:
                os.kill(pid, signal.SIGKILL)
            except ProcessLookupError:
                return
            raise


def launch(argv, data, action, root, env):
    with (root / 'process.log').open('a') as log:
        process = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=log, stderr=log,
                                   env=env, start_new_session=True)
    data['processes'].append({'entry': action['entryId'], 'fragment': action['fragmentId'], 'pid': process.pid})
    return process


def run_hooks(hooks, root, seen, bindings, data, action):
    for hook in hooks:
        command = hook['command']
        signature = json.dumps(command)
        if broken == 'ordering-deduplicates-hooks' and signature in seen:
            continue
        seen.add(signature)
        argv = ['sh', '-c', remap(command, root)] if isinstance(command, str) else [remap(word, root) for word in command]
        for i, word in enumerate(argv):
            for guest, host in bindings.items():
                word = word.replace('http://127.0.0.1:' + str(guest) + '/', 'http://127.0.0.1:' + str(host) + '/')
            argv[i] = word
        env = {'PATH': os.environ['PATH'], 'HOME': str(root)}
        if 'WORKSPACE_DIR' in hook.get('env', []) and data['workspace']:
            env['WORKSPACE_DIR'] = remap('/home/agent/workspace', root)
        if hook.get('background'):
            launch(argv, data, action, root, env)
            continue
        result = subprocess.run(argv, text=True, capture_output=True, check=False, env=env)
        if result.returncode and broken != 'ordering-continues-after-failure':
            raise RuntimeError('hook failed: ' + result.stderr)


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__('localhost', timeout=1)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(str(self.path))


def answered(path):
    try:
        with contextlib.closing(UnixHTTP(path)) as connection:
            connection.request('GET', '/_ping')
            response = connection.getresponse()
            return response.status == 200 and response.read() == b'OK,'
    except (OSError, http.client.HTTPException):
        return False


def boot(data, root):
    socket_root = pathlib.Path(remap('/var/run', root))
    socket_root.mkdir(exist_ok=True)
    for path in ('var/tmp', 'etc/docker', 'var/lib/docker', 'home/agent', 'usr/local/bin'):
        (root / path).mkdir(parents=True, exist_ok=True)
    config = root / 'etc/docker/daemon.json'
    if not config.exists():
        shutil.copyfile(fixtures / 'ordering-workload/daemon.json', config)
    for source, target in [('probe.py', 'usr/local/bin/kit-tck-ordering'), ('dockerd.py', 'dockerd.py')]:
        (root / target).write_text(remap((fixtures / 'ordering-workload' / source).read_text(), root))
    actions = [action for phase in data['plan']['phases'] for step in phase['steps'] for action in (step['actions'] or [])]
    if broken in ('ordering-ignores-order', 'ordering-reverses-provider-hooks'):
        actions = [action for phase in ('prepare', 'install', 'start', 'running')
                   for step in ('attach', 'place', 'execute') for action in reversed(actions)
                   if (action['phase'], action['step']) == (phase, step)]
    if broken == 'ordering-files-before-install':
        actions.sort(key=lambda action: action['action'] != 'files')
    if broken == 'ordering-files-after-execute':
        actions.sort(key=lambda action: action['action'] == 'files')
    if broken == 'ordering-workload-before-startup':
        actions.sort(key=lambda action: action['action'] != 'workload')
    if broken == 'ordering-engine-before-config':
        actions.sort(key=lambda action: action['action'] != 'engine')
    if broken in ('ordering-workspace-late', 'ordering-workspace-failure-hooks'):
        actions.sort(key=lambda action: action['action'] == 'workspace-ready')
    seen, bindings, entries = set(), {}, {}
    for action in actions:
        name = action['action']
        phase = action['phase']
        capability = action.get('capability', {})
        typ = capability.get('type', '')
        config = capability.get('config', {})
        identity = '/'.join((action['entryId'], action['fragmentId'], phase, name))
        if broken == 'ordering-first-entry-only' and typ.endswith('/lifecycle@1'):
            entries.setdefault((typ, phase), action['entryId'])
            if entries[(typ, phase)] != action['entryId']:
                continue
        if action['repetition'] == 'once' and identity in data['once'] and broken != 'ordering-reinstalls':
            continue
        if name == 'workspace-ready':
            destination = pathlib.Path(remap('/home/agent/workspace', root))
            if not destination.exists():
                result = subprocess.run(['git', 'clone', '--quiet', '--no-local', '--', data['workspace'], str(destination)], capture_output=True, text=True)
                if result.returncode:
                    raise RuntimeError('workspace clone failed: ' + result.stderr)
            if broken == 'ordering-workspace-no-access':
                (destination / 'nested/marker').chmod(0)
        elif name == 'workload':
            record = root / 'var/tmp/ordering-entrypoint'
            size = record.stat().st_size if record.exists() else 0
            launch([sys.executable, str(root / 'usr/local/bin/kit-tck-ordering'), 'workload'], data, action, root, os.environ)
            wait_for(lambda: record.exists() and record.stat().st_size > size, 'workload did not record its observations')
        elif typ.endswith('/host-port@1'):
            bindings[config['container']] = config['host']
        elif typ.endswith('/lifecycle@1'):
            if name == 'files':
                if data['boots'] and broken == 'ordering-files-once':
                    continue
                for entry in config.get('files', []):
                    destination = pathlib.Path(remap(entry['path'], root))
                    destination.parent.mkdir(parents=True, exist_ok=True)
                    if destination.exists() and not entry.get('overwrite', True):
                        continue
                    destination.write_text(entry['content'])
            elif name in ('install', 'startup'):
                if name == 'startup' and data['boots'] and broken == 'ordering-startup-once':
                    continue
                run_hooks(config.get(name, []), root, seen, bindings, data, action)
        elif typ.endswith('/dockerd@1') and name == 'engine':
            env = os.environ | {'KIT_TCK_PROBE_UID': str(os.getuid())}
            if broken == 'ordering-launch-without-readiness':
                env['KIT_TCK_READINESS_DELAY'] = '10'
            process = launch([sys.executable, str(root / 'dockerd.py')], data, action, root, env)
            if broken != 'ordering-launch-without-readiness':
                wait_for(lambda: answered(pathlib.Path(remap('/var/run/docker.sock', root))) or process.poll() is not None, 'engine did not become ready')
                if process.poll() is not None:
                    raise RuntimeError('engine exited before becoming ready')
        if action['repetition'] == 'once' and identity not in data['once']:
            data['once'].append(identity)
    data['boots'] += 1


def main():
    if verb == 'create':
        kits, rejected, overrides, workspace = [], [], {}, ''
        i = 0
        while i < len(args):
            if args[i].startswith('--'):
                if args[i] == '--arg':
                    key, value = args[i + 1].split('=', 1)
                    overrides[key] = value
                elif args[i] == '--reject-capability':
                    rejected.append(args[i + 1])
                elif args[i] == '--workspace-source':
                    workspace = args[i + 1]
                i += 2
            else:
                kits.append(args[i])
                i += 1
        plan = load_plan(kits, rejected, overrides, workspace)
        identifier = uuid.uuid4().hex
        directory = base / identifier
        directory.mkdir()
        root = directory / 'root'
        data = new_state(plan, workspace)
        try:
            boot(data, root)
        except Exception:
            stop(data)
            shutil.rmtree(remap('/var/run', root), ignore_errors=True)
            shutil.rmtree(directory)
            raise
        (directory / 'state.json').write_text(json.dumps(data))
        print(identifier)
        return
    identifier, *argv = args
    directory = base / identifier
    root = directory / 'root'
    data = json.loads((directory / 'state.json').read_text())
    if verb == 'exec':
        if argv and argv[0] == '--':
            argv = argv[1:]
        env = dict(os.environ)
        if data['workspace']:
            env['WORKSPACE_DIR'] = remap('/home/agent/workspace', root)
        result = subprocess.run([remap(word, root) for word in argv], capture_output=True, check=False, env=env)
        sys.stdout.buffer.write(result.stdout)
        sys.stderr.buffer.write(result.stderr)
        sys.exit(result.returncode)
    elif verb == 'start':
        boot(data, root)
    elif verb in ('stop', 'rm'):
        stop(data)
        shutil.rmtree(remap('/var/run', root), ignore_errors=True)
        if verb == 'rm':
            for path in root.rglob('*'):
                if path.is_file():
                    path.chmod(0o600)
            shutil.rmtree(directory)
            return
    else:
        raise RuntimeError('unsupported ordering operation: ' + verb)
    (directory / 'state.json').write_text(json.dumps(data))


try:
    main()
except Exception as error:
    print(error, file=sys.stderr)
    sys.exit(1)
