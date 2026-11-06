#!/usr/bin/env python3
"""A sandbox instance model for environment injection and lifecycle probes."""
import json
import os
from pathlib import Path
import sys
import uuid

root, broken, verb, *argv = sys.argv[1:]
root = Path(root) / "sbx-environment"
root.mkdir(exist_ok=True)
NAMES = ("WORKSPACE_DIR", "SANDBOX_NAME", "SANDBOX_ID", "IS_SANDBOX")
WORKSPACE = "/home/sbxagent/project"


def save(state):
    (root / (state["handle"] + ".json")).write_text(json.dumps(state))


def fail(detail, code=1):
    print(detail, file=sys.stderr)
    sys.exit(code)


def emit(env):
    print(json.dumps(env))


def hook_environment(state, phase):
    env = dict(state["environment"])
    if broken == "sbx-missing-" + phase + "-env":
        env.pop("SANDBOX_ID", None)
    if broken == "sbx-empty-workspace-hook" and state["mode"] == "none":
        env["WORKSPACE_DIR"] = ""
    return env


if verb == "create":
    kits, overrides = [], {}
    name, mode, exports = "", "", False
    while argv:
        arg = argv.pop(0)
        if arg == "--name":
            name = argv.pop(0)
        elif arg == "--workspace-mode":
            mode = argv.pop(0)
        elif arg == "--env":
            key, value = argv.pop(0).split("=", 1)
            overrides[key] = value
        elif arg.startswith("--"):
            argv.pop(0)
        else:
            kits.append(Path(arg).name)
    exports = "sbx-environment-exports" in kits
    mode = mode or os.environ.get("KIT_TCK_FAKE_DEFAULT_WORKSPACE", "internal")
    if mode not in ("", "internal", "host", "cloned", "none"):
        fail("unsupported workspace mode", 2)
    reference = "sbx-workspace-reference" in kits
    hooks = "sbx-environment-hooks" in kits
    if mode == "none" and reference and broken != "sbx-expands-absent-workspace":
        fail("missing environment variable WORKSPACE_DIR", 2)
    handle = "env-" + uuid.uuid4().hex
    name = name or handle
    identifier = uuid.uuid4().hex
    if broken == "sbx-reuses-deleted-id":
        identifier = name
    env = {"SANDBOX_NAME": name, "SANDBOX_ID": identifier, "IS_SANDBOX": "1"}
    if mode != "none":
        env["WORKSPACE_DIR"] = WORKSPACE
    if broken == "sbx-missing-identity":
        env.pop("SANDBOX_ID")
    if broken == "sbx-wrong-name":
        env["SANDBOX_NAME"] = "another-sandbox"
    if broken == "sbx-wrong-marker":
        env["IS_SANDBOX"] = "true"
    if broken == "sbx-workspace-from-host" and mode == "host":
        env["WORKSPACE_DIR"] = "/host/source"
    if broken == "sbx-workspace-from-clone" and mode == "cloned":
        env["WORKSPACE_DIR"] = "/clone/source"
    if broken == "sbx-workspace-from-internal" and mode == "internal":
        env["WORKSPACE_DIR"] = "/default/workspace"
    if broken == "sbx-empty-absent-workspace" and mode == "none":
        env["WORKSPACE_DIR"] = ""
    if broken == "sbx-no-image-no-injection" and "sbx-environment-clean" in kits:
        env = {}
    if broken == "sbx-image-wins" and not exports and "sbx-environment" in kits:
        env.update(dict(zip(NAMES, ("/image/workspace", "image-name", "image-id", "image-marker"))))
    if broken == "sbx-image-id-wins" and not exports and "sbx-environment" in kits:
        env["SANDBOX_ID"] = "image-id"
    if broken == "sbx-argument-wins" and exports and not overrides:
        env.update(dict(zip(NAMES, ("/argument/workspace", "argument-name", "argument-id", "argument-marker"))))
    if broken == "sbx-argument-id-wins" and exports and not overrides:
        env["SANDBOX_ID"] = "argument-id"
    if broken == "sbx-override-wins" and any(overrides.values()):
        env.update(overrides)
    if broken == "sbx-override-id-wins" and any(overrides.values()):
        env["SANDBOX_ID"] = overrides["SANDBOX_ID"]
    if broken == "sbx-empty-override-wins" and overrides and not any(overrides.values()):
        env.update(overrides)
    if broken == "sbx-inherits-host":
        env.update({key: os.environ[key] for key in NAMES if key in os.environ})
    expanded = dict(env) if hooks else {}
    if not reference:
        expanded.pop("WORKSPACE_DIR", None)
    if broken == "sbx-expands-before-injection":
        expanded["SANDBOX_ID"] = "image-id"
    state = {"handle": handle, "name": name, "mode": mode, "environment": env,
             "expanded": expanded, "hooks": hooks, "scratch": False,
             "agent": dict(env)}
    if broken == "sbx-injects-after-agent":
        state["agent"] = {}
    if hooks:
        for phase in ("install", "startup"):
            state[phase] = hook_environment(state, phase)
            state[phase + "-restricted"] = dict(env) if broken == "sbx-leaks-unrequested-env" else {}
    save(state)
    print(handle)
    sys.exit(0)

handle = argv.pop(0)
path = root / (handle + ".json")
state = json.loads(path.read_text())
if verb == "rm":
    path.unlink()
elif verb in ("stop", "start", "recreate"):
    if verb != "stop":
        env = state["environment"]
        if broken == "sbx-id-changes-on-" + verb:
            env["SANDBOX_ID"] = uuid.uuid4().hex
        if broken != "sbx-no-agent-on-" + verb:
            state["agent"] = dict(env)
        if state["hooks"]:
            state["startup"] = hook_environment(state, "startup")
            if broken == "sbx-reexpands-restart" and verb == "start":
                state["expanded"]["SANDBOX_NAME"] = os.environ["SANDBOX_NAME"]
        if verb == "recreate" and broken != "sbx-keeps-container-layer":
            state["scratch"] = False
        save(state)
elif verb == "exec":
    overrides, cwd = {}, ""
    while argv[0] != "--":
        arg = argv.pop(0)
        if arg == "--cwd":
            cwd = argv.pop(0)
        elif arg == "--env":
            key, value = argv.pop(0).split("=", 1)
            overrides[key] = value
        else:
            fail("unknown exec option")
    argv.pop(0)
    if argv.pop(0) != "kit-tck-sbx-env":
        fail("unknown environment probe")
    action = argv.pop(0)
    if action == "read":
        emit(state[argv[0]])
    elif action == "expanded":
        emit(state["expanded"])
    elif action in ("current", "process"):
        env = dict(state["environment"])
        if broken == "sbx-process-override-wins":
            env.update(overrides)
        if broken == "sbx-process-id-override-wins" and "SANDBOX_ID" in overrides:
            env["SANDBOX_ID"] = overrides["SANDBOX_ID"]
        if broken == "sbx-cwd-changes-workspace" and cwd:
            env["WORKSPACE_DIR"] = cwd
        if action == "process":
            emit({"environment": env, "cwd": WORKSPACE if broken == "sbx-ignores-process-cwd" else cwd})
        else:
            emit(env)
    elif action == "clear":
        for key in ("agent", "startup", "startup-restricted"):
            state.pop(key, None)
        save(state)
    elif action == "scratch":
        state["scratch"] = True
        save(state)
    elif action == "fresh":
        sys.exit(int(state["scratch"]))
    else:
        fail("unknown environment probe action")
else:
    fail("unknown environment verb")
