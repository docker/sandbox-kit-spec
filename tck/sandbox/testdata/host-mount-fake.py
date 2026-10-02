#!/usr/bin/env python3
"""Host-directory storage for the fake adapter, with isolated mutations."""
import hashlib
import json
from pathlib import Path
import re
import shutil
import sys

verb, state, claims, broken, *args = sys.argv[1:]
state = Path(state)
store = state / '.host-mounts'
supported = 'com.docker.sandbox/host-mount@1' in claims.split(',')
reused_handle = 'hm-' + '0' * 64


def fixtures(kits):
    return [kit for kit in kits if Path(kit).name.startswith('host-mount') and Path(kit).name != 'host-mount-volume']


def repository_identity(kit):
    if broken != 'host-mount-keys-full-reference' and Path(kit).name in ['host-mount-version-v1', 'host-mount-version-v2']:
        # These fixture inputs stand for two published versions of one
        # suite-owned repository, as required by the adapter contract.
        return str(Path(kit).parent / 'host-mount-version')
    return kit


def directory(handle):
    if not re.fullmatch(r'hm-[0-9a-f]{64}', handle):
        raise ValueError('invalid host-directory handle')
    if broken == 'host-mount-reused-handle' and handle == reused_handle and store.exists():
        # Distinct directories expose one ambiguous opaque handle. Each
        # removal affects only one, so deduplication cannot prove cleanup.
        for location in sorted(store.iterdir()):
            if (location / 'record.json').exists():
                return location
    return store / handle


def records(kit):
    result = []
    if store.exists():
        for location in sorted(store.iterdir()):
            metadata = location / 'record.json'
            if metadata.exists():
                record = json.loads(metadata.read_text())
                if repository_identity(record['kit']) == repository_identity(kit) or broken == 'host-mount-uses-display-identity':
                    entry = {key: record[key] for key in ['id', 'path', 'hostPath']}
                    if broken == 'host-mount-reused-handle':
                        entry['id'] = reused_handle
                    result.append(entry)
    return result


if verb == 'preflight':
    mount_path, *kits = args
    requested = fixtures(kits)
    if len(requested) > 1 or (requested and any(Path(kit).name == 'host-mount-volume' for kit in kits)):
        if broken != 'host-mount-merges-conflicts':
            print('refusing: host mount destination has multiple storage owners', file=sys.stderr)
            sys.exit(2)
    if not supported and requested:
        if any(Path(kit).name != 'host-mount-optional' for kit in requested):
            if broken not in ['host-mount-accepts-unclaimed', 'accepts-required-unclaimed']:
                print('refusing: required host-mount@1 is unclaimed', file=sys.stderr)
                sys.exit(2)
        elif broken == 'host-mount-refuses-optional':
            print('refusing: optional host mount', file=sys.stderr)
            sys.exit(2)
elif verb == 'create':
    sandbox, mount_path, mount_mode, *kits = args
    requested = fixtures(kits)
    if not requested:
        sys.exit(0)
    kit = requested[0]
    root = state / sandbox
    if not supported and broken != 'host-mount-unclaimed-grant':
        record = {'path': 'capabilities[0]', 'source': {'kit': Path(kit).name, 'path': 'capabilities[0]'}, 'members': ['capabilities[0]'], 'rejected': ['capabilities[0]']}
        skipped = [] if broken == 'host-mount-omits-skip' else [record]
        (root / 'selection.json').write_text(json.dumps({'selection': {'selected': [], 'skipped': skipped}, 'surface': {}}))
        sys.exit(0)
    identity = 'display-label' if broken == 'host-mount-uses-display-identity' else repository_identity(kit)
    if broken == 'host-mount-per-sandbox':
        identity += sandbox
    key_path = '' if broken == 'host-mount-ignores-path' else mount_path
    handle = 'hm-' + hashlib.sha256((identity + '\0' + key_path).encode()).hexdigest()
    location = directory(handle)
    fresh = not location.exists()
    location.mkdir(parents=True, exist_ok=True)
    data = location / 'data'
    data.mkdir(exist_ok=True)
    if fresh or broken == 'host-mount-resets-mode':
        data.chmod(0o755 if broken == 'host-mount-ignores-mode' else int(mount_mode, 8))
    record = {'id': handle, 'kit': kit, 'path': mount_path, 'hostPath': str(data)}
    (location / 'record.json').write_text(json.dumps(record))
    (root / 'host-mount.json').write_text(json.dumps(record))
    selection = {'path': 'capabilities[0]', 'source': {'kit': Path(kit).name, 'path': 'capabilities[0]'}, 'members': ['capabilities[0]']}
    surface = {'storagePaths' if broken == 'host-mount-grant-as-volume' else 'hostMountPaths': [mount_path]}
    (root / 'selection.json').write_text(json.dumps({'selection': {'selected': [selection], 'skipped': []}, 'surface': surface}))
    if Path(kit).name == 'host-mount-hooks':
        target = data
        if broken == 'host-mount-after-hooks':
            target = root / 'late-host-mount'
            target.mkdir()
        for name in ['install', 'startup']:
            (target / name).write_text('mounted')
elif verb == 'probe':
    sandbox, operation, mount_path, name, value = args
    root = state / sandbox
    record = json.loads((root / 'host-mount.json').read_text())
    if mount_path != record['path']:
        raise ValueError('probe path does not match the mounted destination')
    data = Path(record['hostPath'])
    if broken == 'host-mount-after-hooks' and name in ['install', 'startup']:
        data = root / 'late-host-mount'
    if operation in ['write', 'writable']:
        if broken == 'host-mount-root-unwritable':
            print('host mount root is not writable by the agent', file=sys.stderr)
            sys.exit(1)
        (data / name).write_text(value)
        if operation == 'writable':
            print(1000)
    elif operation == 'read':
        sys.stdout.write((data / name).read_text())
    elif operation == 'absent':
        sys.exit(1 if (data / name).exists() else 0)
    elif operation == 'mode':
        print(format(data.stat().st_mode & 0o7777, 'o'))
    elif operation == 'set-mode':
        if broken == 'host-mount-no-guest-chmod':
            print('host-backed filesystem does not support guest chmod', file=sys.stderr)
            sys.exit(1)
        data.chmod(int(value, 8))
    else:
        raise ValueError('unknown probe operation')
elif verb == 'list':
    print(json.dumps([] if broken == 'host-mount-not-listed' else records(args[0])))
elif verb == 'read':
    handle, relative_path = args
    if broken == 'host-mount-not-host-visible':
        sys.exit(1)
    if Path(relative_path).is_absolute() or '..' in Path(relative_path).parts:
        raise ValueError('host read needs a relative fixture filename')
    sys.stdout.write((directory(handle) / 'data' / relative_path).read_text())
elif verb == 'remove':
    if broken == 'host-mount-remove-noop':
        sys.exit(0)
    if broken == 'host-mount-removes-metadata-only':
        (directory(args[0]) / 'record.json').unlink(missing_ok=True)
    else:
        if broken == 'host-mount-removes-other-kit':
            metadata = directory(args[0]) / 'record.json'
            if metadata.exists():
                removed = json.loads(metadata.read_text())
                for location in list(store.iterdir()):
                    other_metadata = location / 'record.json'
                    if other_metadata.exists():
                        other = json.loads(other_metadata.read_text())
                        if other['path'] == removed['path'] and repository_identity(other['kit']) != repository_identity(removed['kit']):
                            shutil.rmtree(location)
        shutil.rmtree(directory(args[0]), ignore_errors=True)
        if store.exists() and not any(store.iterdir()):
            store.rmdir()
elif verb == 'sandbox-rm':
    metadata = state / args[0] / 'host-mount.json'
    if broken == 'host-mount-lost-on-removal' and metadata.exists():
        handle = json.loads(metadata.read_text())['id']
        shutil.rmtree(directory(handle), ignore_errors=True)
else:
    raise ValueError('unknown fake host-directory operation')
