#!/usr/bin/env python3
"""Stateful FUSE grants for tests of the conformance suite itself."""
import json
from pathlib import Path
import sys
import uuid

state, claims, broken, verb, *args = sys.argv[1:]
capability = "com.docker.sandbox/fuse@1"
requesters = ("fuse-first", "fuse-second")
root = Path(state) / "fuse"
root.mkdir(parents=True, exist_ok=True)


def fixtures_of(arguments):
    fixtures = []
    while arguments:
        value = arguments.pop(0)
        if value.startswith("--"):
            arguments.pop(0)
        else:
            fixtures.append(Path(value).name)
    return fixtures


def accept(fixtures):
    requested = any(f in requesters for f in fixtures)
    if requested and capability not in claims.split(","):
        print("refusing: " + capability, file=sys.stderr)
        sys.exit(2)
    return requested


def save():
    record.write_text(json.dumps(data))


if verb == "create":
    fixtures = fixtures_of(args)
    accepted = accept(fixtures)
    identity = "fuse-" + uuid.uuid4().hex
    record = root / (identity + ".json")
    data = {"fixtures": fixtures, "accepted": accepted, "access": accepted, "partial": None}
    save()
    print(identity)
    sys.exit(0)

identity = args.pop(0)
record = root / (identity + ".json")
if verb == "rm":
    record.unlink(missing_ok=True)
    sys.exit(0)
data = json.loads(record.read_text())
if verb == "selection":
    location = "capabilities[0]"
    selected = [{"path": location, "source": {"kit": f, "path": location}, "members": [location]}
                for f in data["fixtures"] if f in requesters and data["accepted"]]
    print(json.dumps({"selection": {"selected": selected, "skipped": []},
                      "surface": {"services": [capability] if data["accepted"] else []}}))
elif verb in ("start", "stop"):
    pass
elif verb == "recreate":
    if broken == "fuse-recreate-noop":
        sys.exit(0)
    fixtures = fixtures_of(args) or data["fixtures"]
    # A refused replacement leaves the current composition and grant intact.
    accepted = accept(fixtures)
    if broken == "fuse-refuses-corrupts-grant":
        data["access"] = not data["access"]
        save()
        print("refusing: " + capability, file=sys.stderr)
        sys.exit(2)
    previous = data["access"]
    data.update(fixtures=fixtures, accepted=accepted, access=accepted)
    # A regrant that restores the device but not the rest of the grant.
    partial = {"fuse-regrant-drops-cap": "cap", "fuse-regrant-blocks-mount": "mount"}.get(broken)
    data["partial"] = partial if accepted else None
    if broken in ("fuse-retains-withdrawn", "fuse-withdrawal-silent") and not accepted:
        data["access"] = previous
    if broken == "fuse-addition-unusable" and accepted:
        data["access"] = False
    if broken == "fuse-removes-other-contributor" and "fuse-second" in fixtures and "fuse-first" not in fixtures:
        data["access"] = False
    save()
elif verb == "exec":
    if args and args[0] == "--":
        args.pop(0)
    if args == ["kit-tck-fuse", "device"]:
        # The device is still there, but the answer is lost: an empty reply
        # must not pass for a withdrawn grant.
        if broken == "fuse-withdrawal-silent" and not data["accepted"]:
            sys.exit(0)
        print("yes" if data["access"] else "no")
    elif args in (["kit-tck-fuse", "cap"], ["kit-tck-fuse-mount"]):
        probe = "cap" if args[0] == "kit-tck-fuse" else "mount"
        print("yes" if data["access"] and data["partial"] != probe else "no")
    else:
        raise SystemExit("unsupported FUSE fake exec: " + repr(args))
else:
    raise SystemExit("unsupported FUSE fake verb: " + verb)
