#!/usr/bin/env python3
"""Instance storage for the volume fixtures, with independently broken duties.

The fake models only its shipped fixtures. Guest probes operate on real
directories; request records choose mounts but cannot stand in for data.
"""
import hashlib
import json
import posixpath
import re
import shutil
import sys
import uuid
from fractions import Fraction
from pathlib import Path

state, claims, broken, verb, *argv = sys.argv[1:]
permissions, _, mutation = broken.partition('+')
if mutation:
    broken = mutation
state = Path(state)
instances = state / 'volume-instances'
storage = state / 'volume-storage'
observations = state / 'volume-observations'
instances.mkdir(parents=True, exist_ok=True)
storage.mkdir(parents=True, exist_ok=True)
observations.mkdir(parents=True, exist_ok=True)
primary = '/var/tmp/kit-tck-volume'
secondary = primary + '-other'


def refuse(detail):
    print('refusing: ' + detail, file=sys.stderr)
    sys.exit(2)


def parse(args):
    kits, overrides, alias = [], {}, ''
    while args:
        value, *args = args
        if value == '--arg':
            name, setting = args.pop(0).split('=', 1)
            overrides[name] = setting
        elif value == '--name':
            alias = args.pop(0)
        elif value.startswith('--'):
            raise ValueError('unsupported volume fixture option: ' + value)
        else:
            kits.append(value)
    return kits, overrides, alias


def size_value(value):
    value = value.lower().replace(' ', '')
    if not value:
        return None
    number = value.rstrip('kmgti b')
    suffix = value[len(number):]
    power = 'kmgt'.index(suffix[0]) + 1 if suffix else 0
    return Fraction(number) * 1024 ** power


def equivalent(a, b):
    size_a, size_b = size_value(a['size']), size_value(b['size'])
    mode_a = int(a['mode'], 8) if a['mode'] else None
    mode_b = int(b['mode'], 8) if b['mode'] else None
    if broken == 'volume-compares-size-spelling':
        size_a, size_b = a['size'], b['size']
    if broken == 'volume-compares-mode-spelling':
        mode_a, mode_b = a['mode'], b['mode']
    if broken == 'volume-rounds-size':
        size_a = float(size_a) if size_a is not None else None
        size_b = float(size_b) if size_b is not None else None
    if broken == 'volume-defaults-unspecified-size':
        size_a, size_b = size_a or 0, size_b or 0
    if broken == 'volume-defaults-unspecified-mode':
        mode_a, mode_b = mode_a or 0, mode_b or 0
    same_size = size_a == size_b
    same_mode = mode_a == mode_b
    if broken == 'volume-wildcard-size' and (size_a is None or size_b is None):
        same_size = True
    if broken == 'volume-wildcard-mode' and (mode_a is None or mode_b is None):
        same_mode = True
    for field in ['size', 'mode', 'tmpfs']:
        if (broken == 'volume-compares-' + field + '-presence'
                and a['_' + field + '_present'] != b['_' + field + '_present']):
            return False
    return same_size and same_mode and a['tmpfs'] == b['tmpfs']


def setting(entry, field, descriptor, overrides):
    # Read the shipped fixture's scalar, including its argument default.
    # Literal settings must not inherit overrides for unrelated Kit args.
    config = entry.split('    config:', 1)[1]
    match = re.search(r'\b' + field + r':\s*("[^"]*"|[^,\s}]+)', config)
    if not match:
        return '', False
    value = match[1].strip('"')
    argument = re.fullmatch(r'\$\{\{ kit\.args\.([a-z_]+) \}\}', value)
    if argument:
        default = re.search(r'^  ' + argument[1] + r':\n    default: ([^\n]+)$', descriptor, re.M)
        value = overrides.get(argument[1], default[1].strip('"'))
    return value, True


def requests(kits, overrides):
    result = {}
    sources = {}
    optionalities = {}
    metadata = {}
    contributions = 0
    for kit in kits:
        name = Path(kit).name
        if not name.startswith('volume-state'):
            continue
        if 'com.docker.sandbox/volume@1' not in claims.split(','):
            refuse('required volume@1 is unclaimed')
        contributions += 1
        descriptor = (Path(kit) / (name + '.yaml')).read_text()
        entry = descriptor.split('  - type: com.docker.sandbox/volume@1', 1)[1].split('  - type:', 1)[0]
        optional = any(line.strip() == 'optional: true' for line in entry.splitlines())
        path, _ = setting(entry, 'path', descriptor, overrides)
        if broken != 'volume-raw-paths':
            path = posixpath.normpath(path)
        size, has_size = setting(entry, 'size', descriptor, overrides)
        mode, has_mode = setting(entry, 'mode', descriptor, overrides)
        tmpfs_value, has_tmpfs = setting(entry, 'tmpfs', descriptor, overrides)
        tmpfs = tmpfs_value == 'true'
        display = {field: next((line.strip() for line in entry.splitlines()
                               if line.lstrip().startswith(field + ':')), '')
                   for field in ['name', 'description']}
        source = next((line.strip() for line in entry.splitlines()
                       if line.lstrip().startswith('source:')), '')
        for path, config, source, optional, display in [
            (path, dict(size=size, mode=mode, tmpfs=tmpfs,
                        _size_present=has_size, _mode_present=has_mode,
                        _tmpfs_present=has_tmpfs), source, optional, display),
            (secondary, dict(size='1g', mode='0700', tmpfs=tmpfs,
                             _size_present=True, _mode_present=True,
                             _tmpfs_present=tmpfs), '', False, {}),
        ]:
            if path in result and broken == 'volume-compares-provenance' and sources[path] != source:
                refuse('diagnostic provenance differs at ' + path)
            if path in result and broken == 'volume-compares-optionality' and optionalities[path] != optional:
                refuse('request optionality differs at ' + path)
            for field in ['name', 'description']:
                if (path in result and broken == 'volume-compares-' + field
                        and metadata[path].get(field) != display.get(field)):
                    refuse('display ' + field + ' differs at ' + path)
            if path in result and not equivalent(result[path], config) and broken != 'volume-merges-conflicts':
                refuse('conflicting storage configurations at ' + path)
            result[path] = config
            sources[path] = source
            optionalities[path] = optionalities.get(path, True) and optional
            metadata[path] = display
    if contributions > 1 and broken == 'volume-refuses-matching':
        refuse('matching volume requests rejected')
    return result


def record_path(sandbox):
    return instances / (sandbox + '.json')


def load(sandbox):
    return json.loads(record_path(sandbox).read_text())


def destination(record, path, writable=False):
    key = 'one-path' if broken == 'volume-ignores-path' and not writable else path
    digest = hashlib.sha256(key.encode()).hexdigest()
    return storage / record['storage'] / ('writable' if writable else 'volumes') / digest


def save(sandbox, record):
    record_path(sandbox).write_text(json.dumps(record))
    # Keep the observation handle independent of live instance metadata;
    # removal must be checked against backing allocations, not records.
    (observations / sandbox).write_text(record['storage'])
    root = storage / record['storage']
    root.mkdir(parents=True, exist_ok=True)
    (root / 'requests.json').write_text(json.dumps(record['allocated']))


def clear_tmpfs(record, event):
    for path, config in record['allocated'].items():
        if config['tmpfs']:
            target = destination(record, path)
            shutil.rmtree(target, ignore_errors=True)
            target.mkdir(parents=True)
            target.chmod(int(config['mode'] or '0700', 8))
            if permissions == 'volume-fresh-tmpfs-unreadable-' + event:
                target.with_suffix('.inaccessible').touch()


def run_hooks(record, event):
    if (not any(Path(kit).name == 'volume-state-hooks' for kit in record['kits'])
            or 'com.docker.sandbox/lifecycle@1' not in claims.split(',')):
        return
    path = posixpath.normpath(record['args'].get('volume_path', primary))
    late = (broken == 'volume-after-hooks'
            or (event == 'start' and broken == 'volume-after-restart-hooks')
            or (event == 'recreate' and broken == 'volume-after-recreate-hooks'))
    target = destination(record, path, late)
    target.mkdir(parents=True, exist_ok=True)
    for hook in (['install', 'startup'] if event == 'create' else ['startup']):
        (target / hook).write_text('mounted')


def apply(record, kits, overrides, recreating=False):
    candidate = requests(kits, overrides)
    for path, config in candidate.items():
        previous = record['allocated'].get(path)
        if broken == 'volume-forgets-dormant-config' and path not in record['selected']:
            previous = None
        if previous and not equivalent(previous, config):
            if broken == 'volume-refusal-destroys-container':
                shutil.rmtree(storage / record['storage'] / 'writable', ignore_errors=True)
            if broken == 'volume-refusal-destroys-storage':
                shutil.rmtree(storage / record['storage'] / 'volumes', ignore_errors=True)
            if broken == 'volume-refusal-resets-mode':
                for child in destination(record, path).iterdir():
                    if child.is_file():
                        child.chmod(int(previous['mode'] or '0700', 8))
            if broken != 'volume-ignores-recreate-config':
                refuse('retained volume configuration changed at ' + path)
    if recreating:
        if broken != 'volume-keeps-writable-layer':
            shutil.rmtree(storage / record['storage'] / 'writable', ignore_errors=True)
        if broken == 'volume-keys-on-kit' and record['kits'] != kits:
            shutil.rmtree(storage / record['storage'] / 'volumes', ignore_errors=True)
        if broken == 'volume-keys-on-wrapper' and any(Path(kit).name == 'volume-state-published' for kit in kits):
            shutil.rmtree(storage / record['storage'] / 'volumes', ignore_errors=True)
        if broken != 'volume-tmpfs-persists-recreate':
            clear_tmpfs(record, 'recreate')
        if broken == 'volume-loses-renamed-away-data' and primary + '-renamed' in candidate:
            (destination(record, primary) / 'marker').unlink(missing_ok=True)
    record['kits'], record['args'], record['selected'] = kits, overrides, candidate
    for path, config in candidate.items():
        target = destination(record, path)
        fresh = not target.exists()
        target.mkdir(parents=True, exist_ok=True)
        if fresh or broken == 'volume-resets-mode':
            target.chmod(int(config['mode'] or '0700', 8))
        if not fresh and broken == 'volume-resets-mode':
            for child in target.iterdir():
                if child.is_file():
                    child.chmod(int(config['mode'] or '0700', 8))
        if fresh and broken == 'volume-copies-image':
            (target / 'image-marker').write_text('image')
        record['allocated'][path] = config
    if broken == 'volume-forgets-undeclared':
        for path in list(record['allocated']):
            if path not in candidate:
                shutil.rmtree(destination(record, path), ignore_errors=True)
                del record['allocated'][path]
    for path in [primary, secondary, primary + '-control']:
        destination(record, path, True).mkdir(parents=True, exist_ok=True)
    run_hooks(record, 'recreate' if recreating else 'create')


if verb == 'create':
    kits, overrides, alias = parse(argv)
    sandbox = 'volume-' + uuid.uuid4().hex
    root = 'shared' if broken == 'volume-shares-instances' else sandbox
    if broken == 'volume-reuses-name':
        root = alias or sandbox
    record = dict(storage=root, alias=alias, kits=[], args={}, allocated={}, selected={})
    previous = storage / root / 'requests.json'
    if previous.exists():
        record['allocated'] = json.loads(previous.read_text())
    apply(record, kits, overrides)
    save(sandbox, record)
    print(sandbox)
elif verb == 'volume-paths':
    handle = observations / argv[0]
    root = storage / handle.read_text() if handle.exists() else None
    if root is None or not (root / 'requests.json').exists():
        print('[]')
    else:
        record = dict(storage=root.name)
        allocated = json.loads((root / 'requests.json').read_text())
        print(json.dumps(sorted(path for path, config in allocated.items()
                                if not config['tmpfs'] and destination(record, path).exists())))
elif verb == 'rm':
    sandbox = argv[0]
    record = load(sandbox)
    if broken != 'volume-retains-after-removal':
        if broken not in ['volume-reuses-name', 'volume-orphans-after-removal']:
            shutil.rmtree(storage / record['storage'], ignore_errors=True)
        record_path(sandbox).unlink()
elif verb == 'recreate':
    sandbox, *args = argv
    record = load(sandbox)
    kits, overrides, _ = parse(args)
    if kits and (broken == 'volume-ignores-replacement-kits'
                 or (broken == 'volume-ignores-published-kit'
                     and any(Path(kit).name == 'volume-state-published' for kit in kits))):
        kits = record['kits']
    apply(record, kits or record['kits'], record['args'] | overrides, True)
    save(sandbox, record)
elif verb in ['stop', 'start']:
    record = load(argv[0])
    if verb == 'stop' and broken != 'volume-tmpfs-persists-stop':
        clear_tmpfs(record, 'stop')
    if verb == 'start':
        run_hooks(record, 'start')
elif verb == 'exec':
    sandbox, separator, probe, *args = argv
    if separator != '--':
        raise ValueError('unexpected volume probe')
    record = load(sandbox)
    if probe == 'cat':
        markers = {
            '/var/tmp/kit-tck-volume-workload': ('volume-workload', 'replacement'),
            '/usr/share/kit-tck-volume-published': ('volume-state-published', 'published'),
        }
        kit, value = markers[args[0]]
        if not any(Path(ref).name == kit for ref in record['kits']):
            sys.exit(1)
        print(value)
        sys.exit(0)
    if probe != 'kit-tck-volume':
        raise ValueError('unexpected volume probe')
    operation, path, *args = args
    path = posixpath.normpath(path)
    mounted = path in record['selected'] or (broken == 'volume-mounts-undeclared' and path in record['allocated'])
    target = destination(record, path, not mounted)
    name = args[0] if args else ''
    value = args[1] if len(args) > 1 else ''
    inaccessible_tmpfs = (mounted and permissions.startswith('volume-fresh-tmpfs-unreadable-')
                          and target.with_suffix('.inaccessible').exists())
    inaccessible_root = (mounted and (permissions == 'volume-root-not-readable'
                         or (permissions == 'volume-renamed-root-not-readable'
                             and path == primary + '-renamed')))
    if operation in ['writable', 'readable']:
        if broken == 'volume-setup-probe-error' or not target.is_dir():
            sys.exit(1)
        if inaccessible_tmpfs or inaccessible_root or (mounted and operation == 'writable'
                                                      and permissions == 'volume-root-not-writable'):
            sys.exit(3)
    elif operation == 'write':
        if inaccessible_tmpfs or inaccessible_root or (mounted and permissions == 'volume-root-not-writable'):
            raise PermissionError('the agent cannot write the mount root')
        target.mkdir(parents=True, exist_ok=True)
        (target / name).write_text(value)
    elif operation == 'read':
        if inaccessible_tmpfs or inaccessible_root or (mounted and broken == 'volume-retained-read-denied'):
            raise PermissionError('the agent cannot read the mount root')
        sys.stdout.write((target / name).read_text())
    elif operation == 'absent':
        if inaccessible_tmpfs or inaccessible_root:
            raise PermissionError('the agent cannot inspect the volume root')
        sys.exit(1 if (target / name).exists() else 0)
    elif operation == 'empty':
        if inaccessible_root:
            raise PermissionError('the agent cannot list the mount root')
        sys.exit(1 if not target.exists() or any(target.iterdir()) else 0)
    elif operation == 'mounted':
        sys.exit(0 if mounted else 1)
    elif operation == 'unmounted':
        sys.exit(1 if mounted else 0)
    elif operation == 'mode':
        entry = target / name if name else target
        print(format(entry.stat().st_mode & 0o7777, 'o'))
    elif operation == 'set-mode':
        if not name and broken == 'volume-root-not-owned':
            raise PermissionError('the agent does not own the mount root')
        entry = target / name if name else target
        entry.chmod(int(value, 8))
    else:
        raise ValueError('unknown volume probe operation')
else:
    raise ValueError('unexpected volume verb: ' + verb)
