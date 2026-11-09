#!/usr/bin/env python3
"""Run bounded selection verification and real-code negative controls."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[3]
CHECK = ROOT / "skills/verify-capability-selection/scripts/check.go"
SOURCE = ROOT / "spec/groups.go"

# Go overlays compile each broken source in isolation. The checkout is never
# edited, so interruption cannot leave a product-code mutation behind.
MUTATIONS = [
    (
        "atomic-selection",
        "if len(record.Rejected) > 0 {",
        "if len(record.Rejected) > 0 && (item.Group == nil || len(record.Rejected) == len(members)) {",
    ),
    ("required-rejection", "if !optional {", "if !optional && len(members) == 0 {"),
    ("declaration-order", "selected = append(selected, c)", "selected = append([]Capability{c}, selected...)"),
    ("cancellation", "return Selection{}, err", "return result, err"),
    (
        "target-identity",
        "SelectionTarget{Reference: reference, Path: memberPath}",
        "SelectionTarget{Reference: reference, Path: origin.Path}",
    ),
    (
        "decision-records",
        "record.Decisions = append(record.Decisions, decision)",
        "record.Decisions = append(record.Decisions, CapabilityDecision{Accepted: decision.Accepted})",
    ),
]


def run(overlay=None):
    env = dict(os.environ, GOWORK="off")
    # Use the repository's exact toolchain, including when a newer Go is on PATH.
    version = next(line.split()[1] for line in (ROOT / "go.mod").read_text().splitlines() if line.startswith("go "))
    env["GOTOOLCHAIN"] = f"go{version}"
    command = ["go", "run"]
    if overlay is not None:
        command += ["-overlay", str(overlay)]
    command.append(str(CHECK))
    return subprocess.run(command, cwd=ROOT, env=env, text=True, capture_output=True, timeout=180)


def baseline():
    result = run()
    print(result.stdout, end="", flush=True)
    if result.returncode:
        print(result.stderr, end="", file=sys.stderr)
        raise SystemExit(result.returncode)


def main():
    baseline()
    source = SOURCE.read_text()
    with tempfile.TemporaryDirectory(prefix="kit-selection-verification-") as directory:
        scratch = Path(directory)
        for property_name, before, after in MUTATIONS:
            expected_matches = 2 if property_name == "cancellation" else 1
            if source.count(before) != expected_matches:
                raise SystemExit(f"Source changed: review the {property_name} mutation before rerunning")
            mutated = scratch / "groups.go"
            mutated.write_text(source.replace(before, after))
            overlay = scratch / "overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(SOURCE): str(mutated)}}))
            result = run(overlay)
            output = result.stdout + result.stderr
            marker = f"COUNTEREXAMPLE [{property_name}]"
            if result.returncode == 0 or marker not in output:
                raise SystemExit(f"Negative control {property_name} did not fail as expected:\n{output}")
            print(f"CAUGHT {property_name}\n{output.rstrip()}", flush=True)
    if SOURCE.read_text() != source:
        raise SystemExit("Selection source changed during verification; rerun on a stable checkout")
    print("Rechecking the unchanged implementation after removing overlays", flush=True)
    baseline()


if __name__ == "__main__":
    main()
