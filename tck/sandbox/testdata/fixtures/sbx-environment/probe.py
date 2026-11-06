#!/usr/bin/env python3
"""Record the environment at launch, before the suite attaches a process."""
import json
import os
from pathlib import Path
import sys
import time

NAMES = ("WORKSPACE_DIR", "SANDBOX_NAME", "SANDBOX_ID", "IS_SANDBOX")

def snapshot():
    return {name: os.environ[name] for name in NAMES if name in os.environ}

mode = sys.argv[1]
if mode == "launch":
    Path("/var/tmp/sbx-agent.env").write_text(json.dumps(snapshot()))
    os.execlp("sleep", "sleep", "infinity")
elif mode == "record":
    Path("/var/tmp/sbx-" + sys.argv[2] + ".env").write_text(json.dumps(snapshot()))
elif mode == "read":
    path = Path("/var/tmp/sbx-" + sys.argv[2] + ".env")
    # An adapter may return from create just before the agent is scheduled.
    for _ in range(100):
        if path.exists():
            break
        time.sleep(0.1)
    print(path.read_text())
elif mode == "current":
    print(json.dumps(snapshot()))
elif mode == "process":
    print(json.dumps({"environment": snapshot(), "cwd": os.getcwd()}))
elif mode == "expanded":
    base = Path("/home/sbxagent/.config/sbx-expanded")
    print(json.dumps({name: (base / name).read_text() for name in NAMES
                      if (base / name).exists()}))
elif mode == "clear":
    for name in ("agent", "startup", "startup-restricted"):
        Path("/var/tmp/sbx-" + name + ".env").unlink(missing_ok=True)
elif mode == "scratch":
    Path("/var/tmp/sbx-writable-layer").write_text("discard me")
elif mode == "fresh":
    sys.exit(Path("/var/tmp/sbx-writable-layer").exists())
else:
    sys.exit("unknown environment probe")
