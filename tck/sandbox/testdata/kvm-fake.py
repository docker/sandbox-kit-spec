#!/usr/bin/env python3
"""Stateful KVM grants for tests of the conformance suite itself."""
import json
from pathlib import Path
import sys
import uuid

state, claims, broken, verb, *args = sys.argv[1:]
capability = "com.docker.sandbox/kvm@1"
root = Path(state) / "kvm"
root.mkdir(parents=True, exist_ok=True)


def inputs(arguments):
    fixtures, rejected = [], []
    while arguments:
        value = arguments.pop(0)
        if value.startswith("--"):
            argument = arguments.pop(0)
            if value == "--reject-capability":
                rejected.append(argument)
        else:
            fixtures.append(Path(value).name)
    return fixtures, rejected


def selection(fixtures, accepted):
    result = {"selection": {"selected": [], "skipped": []}, "surface": {"services": [capability] if accepted else []}}
    for fixture in fixtures:
        if fixture not in ("kvm", "kvm-second", "kvm-hooks", "kvm-optional"):
            continue
        location = "capabilities[0]"
        record = {"path": location, "source": {"kit": fixture, "path": location}, "members": [location]}
        if not accepted:
            record["rejected"] = [location]
        result["selection"]["selected" if accepted else "skipped"].append(record)
    return result


def has_request(fixtures):
    return any(f in ("kvm", "kvm-second", "kvm-hooks", "kvm-optional") for f in fixtures)


def select_request(fixtures, rejected):
    supported = capability in claims.split(",")
    accepted = has_request(fixtures) and supported and capability not in rejected
    if broken == "kvm-ignores-required-rejection" and supported:
        accepted = True
    if broken == "accepts-required-unclaimed":
        accepted = True
    required = any(f in ("kvm", "kvm-second", "kvm-hooks") for f in fixtures)
    if required and not accepted:
        print("refusing: " + ("unavailable capability" if broken == "kvm-refusal-omits-type" else capability), file=sys.stderr)
        sys.exit(2)
    if "kvm-optional" in fixtures and not accepted and broken == "kvm-refuses-optional":
        print("refusing: " + capability, file=sys.stderr)
        sys.exit(2)
    return accepted


def save():
    record.write_text(json.dumps(data))


if verb == "create":
    fixtures, rejected = inputs(args)
    accepted = select_request(fixtures, rejected)
    identity = "kvm-" + uuid.uuid4().hex
    record = root / (identity + ".json")
    data = {"fixtures": fixtures, "accepted": accepted, "access": accepted, "startup": accepted, "rejected": rejected}
    if broken == "kvm-late-access":
        data["startup"] = False
    if broken == "kvm-optional-leaks" and not accepted:
        data["access"] = True
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
    result = selection(data["fixtures"], data["accepted"])
    if "kvm-optional" in data["fixtures"] and not data["accepted"]:
        if broken == "kvm-omits-skip":
            result["selection"]["skipped"] = []
        if broken == "kvm-malformed-skip":
            result["selection"]["skipped"][0]["source"]["path"] = "capabilities[1]"
        if broken == "kvm-skipped-service":
            result["surface"]["services"] = [capability]
        if broken == "kvm-skipped-selected":
            result["selection"]["selected"] = result["selection"]["skipped"]
    print(json.dumps(result))
elif verb == "selection-policy":
    _, data["rejected"] = inputs(args)
    save()
elif verb == "start":
    data["startup"] = data["accepted"] and broken != "kvm-restart-late-access"
    if broken == "kvm-restart-loses-access":
        data["access"] = False
    if broken == "kvm-restart-reselects" and capability in data["rejected"]:
        data["accepted"] = False
        data["access"] = False
    save()
elif verb == "stop":
    pass
elif verb == "recreate":
    if broken == "kvm-recreate-noop":
        sys.exit(0)
    fixtures, _ = inputs(args)
    fixtures = fixtures or data["fixtures"]
    # A refused replacement leaves the current composition and grants intact.
    # Future policy is separate from those grants until recreation succeeds.
    accepted = select_request(fixtures, data["rejected"])
    if broken in ("kvm-refuses-transition", "kvm-refuses-corrupts-grant", "kvm-refuses-corrupts-selection"):
        if broken == "kvm-refuses-corrupts-grant":
            data["access"] = not data["access"]
            save()
        if broken == "kvm-refuses-corrupts-selection":
            data["fixtures"] = fixtures
            data["accepted"] = accepted
            save()
        print("refusing: " + capability, file=sys.stderr)
        sys.exit(2)
    previous = data["access"]
    data["fixtures"] = fixtures
    data["accepted"] = accepted
    data["access"] = accepted
    data["startup"] = accepted and broken != "kvm-late-access"
    if broken in ("kvm-retains-withdrawn", "kvm-retains-withdrawn-vsock") and not accepted:
        data["access"] = previous
    if broken == "kvm-addition-unusable" and accepted:
        data["access"] = False
    if broken == "kvm-removes-other-contributor" and "kvm-second" in fixtures and "kvm" not in fixtures:
        data["access"] = False
    save()
elif verb == "exec":
    if args and args[0] == "--":
        args.pop(0)
    if args[0] == "kit-tck-kvm":
        mode = args[1]
        available = data["access"]
        if broken in ("kvm-missing", "kvm-api-version", "kvm-create-vm") and mode in ("kvm", "all"):
            available = False
        if broken in ("kvm-vsock-missing", "kvm-vsock-ioctl") and mode in ("vsock", "all"):
            available = False
        if broken == "kvm-retains-withdrawn-vsock" and not data["accepted"] and mode == "kvm":
            available = False
        if not available:
            if broken == "kvm-probe-crashes":
                print("Traceback: probe could not start", file=sys.stderr)
                sys.exit(1)
            print("unavailable:" + mode + ":device unavailable or ioctl refused", file=sys.stderr)
            sys.exit(3)
        print("usable")
    elif args[0] == "rm" and args[1:] in (
        ["-f", "/var/tmp/kit-tck-workload-kvm"],
        ["-f", "/var/tmp/kit-tck-kvm-hook", "/var/tmp/kit-tck-workload-kvm"],
    ):
        data["startup"] = False
        save()
    elif args[0] == "cat" and args[1] in ("/var/tmp/kit-tck-kvm-hook", "/var/tmp/kit-tck-workload-kvm"):
        if not data["startup"]:
            sys.exit(1)
        print("usable")
    else:
        raise SystemExit("unsupported KVM fake exec: " + repr(args))
else:
    raise SystemExit("unsupported KVM fake verb: " + verb)
