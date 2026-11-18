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
    progressive = {}
    if "context-v2-tool" in kits:
        team = values.get("team", "default-team")
        final = env.get("KIT_CONTEXT_TEAM", "image")
        if broken == "context-v2-no-expansion":
            team = "${{ kit.args.team }}"
        if broken == "context-v2-expands-shell":
            shell = "expanded expanded"
        else:
            shell = "$TEAM ${TEAM}"
        tool = f"tool-context-v2 built {team} {final} {shell}"
        mode = values.get("context_delivery", "progressive")
        if broken == "context-v2-default-inline" and "context_delivery" not in values:
            mode = "inline"
        if broken == "context-v2-ignores-inline":
            mode = "progressive"
        if mode == "inline":
            bodies.append(tool)
        else:
            if broken == "context-v2-progressive-no-expansion":
                tool = "tool-context-v2 built ${{ kit.args.team }} image $TEAM ${TEAM}"
            progressive["/home/agent/.kit-context-v2/tool.md"] = tool
    explicit = "context-v2-profile" in kits
    if explicit:
        bodies.append("profile-context-v2")
    optional = "context-v2-optional" in kits and "com.docker.sandbox/volume@1" not in rejected
    if "context-v2-optional" in kits and (optional or broken == "context-v2-leaks-skipped"):
        bodies.append("optional-context-v2")
    if "context-v2-progressive" in kits:
        if broken == "context-v2-inline-pulls-progressive" and values.get("context_delivery") == "inline":
            bodies.append("omitted-delivery-context-v2")
        else:
            progressive["/home/agent/.kit-context-v2/default.md"] = "omitted-delivery-context-v2"
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
    link_style = os.environ.get("KIT_TCK_FAKE_CONTEXT_LINK_STYLE", "plain")
    if broken in ("context-v2-reference-unattributed", "context-v2-reference-shared-file"):
        link_style = "reference"
    if link_style not in ("plain", "parent"):
        progressive = {file.replace(".md", " guidance.md"): content for file, content in progressive.items()}
    if broken in ("context-v2-shared-progressive-file", "context-v2-reference-shared-file") and progressive:
        progressive = {"/home/agent/.kit-context-v2/shared.md": "\n\n".join(progressive.values())}
    if broken != "context-v2-missing-index":
        links, definitions = [], []
        for file in progressive:
            if broken in ("context-v2-shared-progressive-file", "context-v2-reference-shared-file"):
                owners = ("context-v2-tool", "context-v2-progressive")
            else:
                owners = ("context-v2-tool" if Path(file).name.startswith("tool") else "context-v2-progressive",)
            for kit in owners:
                if broken == "context-v2-unattributed-index":
                    links.append(f"- Kit guidance: {file}")
                elif link_style == "angle":
                    links.append(f"- [{kit}](<{file}>)")
                elif link_style == "wrapped":
                    links.append(f'- [{kit}\n  guidance](\n  <{file}>\n  "Read guidance"\n  )')
                elif link_style == "continued":
                    links.append(f"- `{kit}@1.0.0`\n  [Read guidance](<{file}>)")
                elif link_style == "parent":
                    relative = os.path.relpath(file, "/home/agent/.kit-tck/v2")
                    links.append(f"- **{kit}**: {relative}")
                elif link_style == "reference":
                    label = "Read guidance" if broken == "context-v2-reference-unattributed" else kit
                    links.append(f"- [{label}][{kit}]")
                    definitions.append(f"[{kit}]: <{file}>")
                else:
                    links.append(f"- **{kit}**: {file}")
        body += "\nKits:\n" + "\n".join(links) + "\n\n" + "\n".join(definitions)
    files = dict.fromkeys(profiles, body)
    if broken != "context-v2-no-body-file":
        files.update(progressive)
    if broken == "context-v2-progressive-drops-profile-index":
        files[profiles[-1]] = "existing-context-v2\n" + "\n\n".join(bodies)
    if optional and broken == "context-v2-truncates-optional":
        files["/home/agent/.kit-tck/v2-optional/OPTIONAL.md"] = "base-context-v2"
    launch_bodies = {profile: progressive for profile in profiles}
    if broken == "context-v2-late-body-file":
        launch_bodies = {profile: {} for profile in profiles}
    files["/var/tmp/context-v2-bodies-at-launch.json"] = json.dumps(launch_bodies)
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
