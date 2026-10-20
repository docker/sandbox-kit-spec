#!/usr/bin/env python3
"""A fixture model for inline-context checks, independent of descriptor merging."""
import json
import os
from pathlib import Path
import sys
import time
import uuid

state, claims, broken, verb, *args = sys.argv[1:]
root = Path(state) / "context-v2"
root.mkdir(parents=True, exist_ok=True)
capability = "com.docker.sandbox/agent-context@2"
if verb == "create":
    kits, values, env, rejected = [], {}, {}, []
    while args:
        flag = args.pop(0)
        if flag.startswith("--"):
            value = args.pop(0)
            if flag in ("--arg", "--env"):
                name, text = value.split("=", 1)
                (values if flag == "--arg" else env)[name] = text
            elif flag == "--reject-capability":
                rejected.append(value)
        else:
            kits.append(Path(flag).name)
    if capability not in claims.split(",") and broken != "accepts-required-unclaimed":
        print("refusing: required " + capability, file=sys.stderr)
        sys.exit(2)
    if "context" in kits and broken != "context-v2-upgrades-v1":
        print("refusing: selected agent-context @1 and @2", file=sys.stderr)
        sys.exit(2)
    bodies = ["base-context-v2"]
    if "context-v2-tool" in kits:
        team = values.get("team", "default-team")
        final = env.get("KIT_CONTEXT_TEAM", "image")
        if broken == "context-v2-no-expansion":
            team = "${{ kit.args.team }}"
        if broken == "context-v2-expands-shell":
            shell = "expanded expanded"
        else:
            shell = "$TEAM ${TEAM}"
        bodies.append(f"tool-context-v2 built {team} {final} {shell}")
    explicit = "context-v2-profile" in kits
    if explicit:
        bodies.append("profile-context-v2")
    optional = "context-v2-optional" in kits and "com.docker.sandbox/volume@1" not in rejected
    if "context-v2-optional" in kits and (optional or broken == "context-v2-leaks-skipped"):
        bodies.append("optional-context-v2")
    if broken == "context-v2-drops-body":
        bodies = bodies[:1]
    if broken == "context-v2-reverses-order":
        bodies.reverse()
    body = "\n\n".join(bodies)
    if broken == "context-v2-index-only":
        body = "See /usr/share/sandbox/kit/context-v2/body.txt"
    if broken != "context-v2-overwrites-user":
        body = "existing-context-v2\n" + body
    profiles = ["/home/agent/AGENTS.md"]
    if explicit and broken != "context-v2-legacy-wins":
        profiles = ["/home/agent/.kit-tck/v2/AGENTS.md", "/home/agent/.kit-tck/v2-other/OTHER.md"]
    if broken == "context-v2-drops-profile":
        profiles = profiles[:1]
    if optional:
        profiles.append("/home/agent/.kit-tck/v2-optional/OPTIONAL.md")
    files = dict.fromkeys(profiles, body)
    if optional and broken == "context-v2-truncates-optional":
        files["/home/agent/.kit-tck/v2-optional/OPTIONAL.md"] = "base-context-v2"
    files["/var/tmp/context-v2-ready"] = "ready\n"
    files["ready_after"] = time.monotonic() + float(os.environ.get("KIT_TCK_FAKE_CONTEXT_READY_DELAY", "0"))
    for index in range(3):
        launch = body
        if broken == "context-v2-after-launch" or (broken == "context-v2-second-after-launch" and index == 2):
            launch = "existing-context-v2"
        files[f"/var/tmp/context-v2-at-launch-{index}"] = launch
    identity = "context-v2-" + uuid.uuid4().hex
    (root / (identity + ".json")).write_text(json.dumps(files))
    print(identity)
else:
    identity = args.pop(0)
    record = root / (identity + ".json")
    if verb == "rm":
        record.unlink(missing_ok=True)
    elif verb == "exec":
        if args and args[0] == "--":
            args.pop(0)
        files = json.loads(record.read_text())
        if len(args) == 2 and args[0] == "cat" and (args[1] == "/var/tmp/context-v2-ready" or args[1].startswith("/var/tmp/context-v2-at-launch-")):
            if time.monotonic() < files["ready_after"]:
                sys.exit(1)
        if len(args) == 2 and args[0] == "cat" and args[1] in files:
            print(files[args[1]], end="")
        else:
            sys.exit(1)
    else:
        raise SystemExit("unsupported inline-context fake verb: " + verb)
