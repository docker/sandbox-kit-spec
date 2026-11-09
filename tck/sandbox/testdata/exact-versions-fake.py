#!/usr/bin/env python3
import json
from pathlib import Path
import sys
import uuid

state, claims, broken, verb, *args = sys.argv[1:]
root = Path(state) / "exact-versions"
root.mkdir(parents=True, exist_ok=True)
if verb == "create":
    fixtures, rejected = [], []
    while args:
        value = args.pop(0)
        if value.startswith("--"):
            argument = args.pop(0)
            if value == "--reject-capability":
                rejected.append(argument)
        else:
            fixtures.append(Path(value))
    accepted, denied = [], []
    for fixture in fixtures:
        descriptor = (fixture / (fixture.name + ".yaml")).read_text()
        if "capabilities:\n" not in descriptor:
            continue
        declarations = json.loads(descriptor.split("capabilities:\n", 1)[1])
        for index, capability in enumerate(declarations):
            location = f"capabilities[{index}]"
            request = {"capability": capability, "fixture": fixture.name, "path": location}
            supported = capability["type"] in claims.split(",")
            eligible = supported and capability["type"] not in rejected
            if broken == "exact-ignores-policy" and supported:
                eligible = True
            (accepted if eligible else denied).append(request)
    required = [r for r in denied if not r["capability"].get("optional", False)]
    if required and broken != "exact-skips-required":
        if broken == "exact-required-no-locations":
            print("refusing: unavailable required request", file=sys.stderr)
            sys.exit(2)
        print("refusing: " + " ".join(f'{r["fixture"]} {r["path"]} {r["capability"]["type"]}' for r in required[:1]), file=sys.stderr)
        sys.exit(2)
    if len(accepted) > 1:
        if broken == "exact-prefers-highest" or (broken == "exact-prefers-per-kit" and len({r["fixture"] for r in accepted}) == 1):
            accepted = [max(accepted, key=lambda r: r["capability"]["type"])]
        elif broken == "exact-prefers-required":
            accepted = [min(accepted, key=lambda r: r["capability"].get("optional", False))]
        else:
            details = " ".join(f'{r["fixture"]} {r["path"]} {r["capability"]["type"]}' for r in accepted)
            if broken == "exact-conflict-no-locations":
                details = "incompatible agent-context versions"
            print("refusing: " + details, file=sys.stderr)
            sys.exit(2)
    if broken == "exact-drops-lower-with-higher" and any(r["capability"]["type"].endswith("@2") for r in denied):
        accepted = []
    selection = {"selected": [], "skipped": []}
    for status, requests in (("selected", accepted), ("skipped", denied)):
        for request in requests:
            location = request["path"]
            record = {"path": location, "source": {"kit": request["fixture"], "path": location}, "members": [location]}
            if status == "skipped":
                record["rejected"] = [location]
            selection[status].append(record)
    files = {}
    for request in accepted:
        capability = request["capability"]
        config = capability["config"]
        if (capability["type"].endswith("@1") and broken != "exact-upgrades-single") or (broken == "exact-downgrades-single" and request["fixture"].startswith("exact-context-v2-")):
            body = "/usr/share/sandbox/kit/" + request["fixture"] + "/context.txt"
        else:
            body = config.get("content", "exact-context-v1")
        files[config["directory"] + "/" + config["filename"]] = body
    identity = "exact-versions-" + uuid.uuid4().hex
    (root / (identity + ".json")).write_text(json.dumps({"selection": selection, "surface": {}, "files": files}))
    print(identity)
else:
    identity = args.pop(0)
    record = root / (identity + ".json")
    if verb == "rm":
        record.unlink(missing_ok=True)
    elif verb == "selection":
        result = json.loads(record.read_text())
        result.pop("files")
        print(json.dumps(result))
    elif verb == "exec":
        if args and args[0] == "--":
            args.pop(0)
        files = json.loads(record.read_text())["files"]
        if len(args) == 2 and args[0] == "cat" and args[1] in files:
            print(files[args[1]])
        else:
            sys.exit(1)
    else:
        raise SystemExit("unsupported exact-version fake verb: " + verb)
