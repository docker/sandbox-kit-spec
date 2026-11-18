#!/usr/bin/env python3
"""Collect public release evidence and publish a checked draft, outside sbx."""

import argparse
import difflib
import json
import os
import stat
import re
import subprocess
import sys
from pathlib import Path
from urllib.parse import quote

REPO = "docker/sandbox-kit-spec"
TAG_RE = re.compile(r"v3\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?\Z")
IDENTITY = ("id", "tag_name", "draft", "prerelease", "published_at", "body", "updated_at")


def run(*args):
    return subprocess.check_output(args, text=True)


def api(path, paginate=False, collection=None):
    args = ["gh", "api", path]
    if paginate:
        args.append("--paginate")
    raw = run(*args)
    if not paginate:
        return json.loads(raw)
    # gh streams consecutive JSON documents for pages, including on older
    # supported CLIs that do not offer --slurp.
    decoder = json.JSONDecoder()
    items = []
    while raw.strip():
        page, end = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[end:]
        items.extend(page[collection] if collection else page)
    return items


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n")


def tag(value):
    if not TAG_RE.fullmatch(value):
        raise ValueError("Expected an explicit v3 release tag")
    return value


def release(value):
    data = api(f"repos/{REPO}/releases/tags/{quote(tag(value), safe='')}")
    if data["tag_name"] != value or data["draft"] or not data["published_at"]:
        raise ValueError("Tag must identify an already published release")
    data["body"] = data.get("body") or ""
    return data


def remote_commit(value):
    obj = api(f"repos/{REPO}/git/ref/tags/{quote(tag(value), safe='')}")["object"]
    for _ in range(8):
        if obj["type"] == "commit":
            return obj["sha"]
        if obj["type"] != "tag":
            break
        obj = api(f"repos/{REPO}/git/tags/{obj['sha']}")["object"]
    raise ValueError("Tag does not resolve to a commit")


def git(source, *args):
    return run("git", "-C", str(source), *args).strip()


def collect(args):
    out = args.output
    out.mkdir(parents=True, exist_ok=False)
    current = release(args.tag)
    write_json(out / "release-original.json", current)
    (out / "release-original.md").write_text(current["body"])
    commit = remote_commit(args.tag)
    if git(args.source, "rev-parse", f"refs/tags/{args.tag}^{{commit}}") != commit:
        raise ValueError("Local tag differs from published tag")
    # GoReleaser's metadata supplies the base on the automatic release path.
    # Manual reruns can use the already authored compare link or an explicit base.
    links = re.findall(
        r"https://github\.com/docker/sandbox-kit-spec/compare/"
        r"(v3\.[0-9A-Za-z.-]+)\.\.\.(v3\.[0-9A-Za-z.-]+)", current["body"]
    )
    bases = {base for base, end in links if end == args.tag}
    base = args.base or (next(iter(bases)) if len(bases) == 1 else "")
    if not base:
        raise ValueError("No unambiguous release base; supply the recorded GoReleaser previous tag")
    tag(base)
    if bases and bases != {base}:
        raise ValueError("Requested base disagrees with the published compare link")
    base_commit = remote_commit(base)
    if base == args.tag or git(args.source, "rev-parse", f"refs/tags/{base}^{{commit}}") != base_commit:
        raise ValueError("Invalid or mismatched base tag")
    subprocess.run(["git", "-C", str(args.source), "merge-base", "--is-ancestor", base_commit, commit], check=True)
    snapshot = {"release": current, "commit": commit, "base": base, "base_commit": base_commit}
    write_json(out / "snapshot.json", snapshot)
    # Save partial evidence as it arrives so a failed API request is diagnosable.
    shas = git(args.source, "rev-list", f"{base_commit}..{commit}").splitlines()
    write_json(out / "commits.json", shas)
    (out / "commit-messages.txt").write_text(run("git", "-C", str(args.source), "log", "--format=fuller", f"{base_commit}..{commit}"))
    prs = {}
    for sha in shas:
        for pr in api(f"repos/{REPO}/commits/{sha}/pulls?per_page=100", paginate=True):
            if pr.get("merged_at"):
                prs[pr["number"]] = pr
    write_json(out / "pull-requests.json", list(prs.values()))
    history = api(f"repos/{REPO}/releases?per_page=100", paginate=True)
    # Reachability, not publication date, bounds the useful formatting examples.
    preceding = []
    for item in history:
        if item["draft"] or item["tag_name"] == args.tag or not TAG_RE.fullmatch(item["tag_name"]):
            continue
        result = subprocess.run(["git", "-C", str(args.source), "merge-base", "--is-ancestor", f"refs/tags/{item['tag_name']}^{{commit}}", commit], capture_output=True)
        if result.returncode == 0:
            preceding.append({"tag_name": item["tag_name"], "body": item.get("body", "")})
    write_json(out / "previous-releases.json", preceding)
    write_json(out / "assets.json", api(f"repos/{REPO}/releases/{current['id']}/assets?per_page=100", paginate=True))
    runs = api(f"repos/{REPO}/actions/workflows/release.yaml/runs?head_sha={commit}&per_page=100", paginate=True, collection="workflow_runs")
    write_json(out / "release-runs.json", runs)
    # During the automatic call the containing run is still in progress;
    # its completed publication job, not the whole run, supplies success proof.
    jobs = []
    for item in runs:
        jobs.extend(api(f"repos/{REPO}/actions/runs/{item['id']}/jobs?per_page=100", paginate=True, collection="jobs"))
    write_json(out / "release-jobs.json", jobs)
    # Buildx queries the registry without running an image or requiring Docker in sbx.
    for name, image in (("frontend", f"docker/sandbox-kit:{args.tag[1:]}"), ("floating", "docker/sandbox-kit:3")):
        raw = run("docker", "buildx", "imagetools", "inspect", image, "--raw")
        (out / f"{name}-manifest.json").write_text(raw)
        (out / f"{name}-inspect.txt").write_text(run("docker", "buildx", "imagetools", "inspect", image))
        attestations = run("docker", "buildx", "imagetools", "inspect", image, "--format", "SBOM={{if .SBOM}}present{{else}}absent{{end}} Provenance={{if .Provenance}}present{{else}}absent{{end}}")
        (out / f"{name}-attestations.txt").write_text(attestations)
    (out / "tagged.diff").write_text(run("git", "-C", str(args.source), "diff", base_commit, commit))


def read_regular(path, limit=125000):
    # Open both the output directory and file without following links. The
    # sandbox may have replaced either; never let artifact upload resolve them.
    directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        descriptor = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
        with os.fdopen(descriptor, newline="") as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
                raise ValueError(f"Missing, linked, or oversized output: {path.name}")
            body = stream.read(limit + 1)
            if len(body.encode()) > limit:
                raise ValueError(f"Oversized output: {path.name}")
            return body
    finally:
        os.close(directory)


def protected_parts(body):
    """Only familiar summary sections may be replaced; retain other authored text."""
    chunks = re.split(r"(?m)(?=^## )", body)
    editable = {"## Highlights", "## Compatibility", "## Changelog"}
    for chunk in chunks:
        if not chunk.strip():
            continue
        heading = chunk.splitlines()[0].strip()
        if heading in editable:
            continue
        # The compare link belongs to the release range, not an authored introduction.
        if chunk.strip().startswith("Full changelog:"):
            continue
        yield chunk


def validate(snapshot, output):
    draft = read_regular(output / "release-notes.md")
    evidence = json.loads(read_regular(output / "evidence.json"))
    expected = {"tag": snapshot["release"]["tag_name"], "commit": snapshot["commit"], "base": snapshot["base"], "base_commit": snapshot["base_commit"]}
    if any(evidence.get(k) != v for k, v in expected.items()):
        raise ValueError("Draft evidence identifies a different release range")
    if evidence.get("complete") is not True or evidence.get("gaps") != []:
        raise ValueError("Agent reported incomplete evidence")
    if not isinstance(evidence.get("claims"), list) or not evidence["claims"]:
        raise ValueError("Evidence must explain claims and their public sources")
    for claim in evidence["claims"]:
        if not isinstance(claim, dict) or not claim.get("claim") or not isinstance(claim.get("claim"), str) or not isinstance(claim.get("sources"), list) or not claim["sources"] or not all(isinstance(source, str) and source.strip() for source in claim["sources"]):
            raise ValueError("Each claim needs evidence sources")
    positions = [draft.find(f"## {heading}\n") for heading in ("Highlights", "Compatibility", "Artifacts")]
    if min(positions) < 0 or positions != sorted(positions) or any(draft.count(f"## {heading}\n") != 1 for heading in ("Highlights", "Compatibility", "Artifacts")):
        raise ValueError("Draft must follow Highlights, Compatibility, Artifacts format")
    compare = f"https://github.com/{REPO}/compare/{snapshot['base']}...{expected['tag']}"
    if compare not in draft:
        raise ValueError("Draft omits the verified full changelog link")
    if len(draft.strip()) < 200 or len(draft.encode()) > 125000:
        raise ValueError("Draft is empty or exceeds GitHub release body limits")
    for part in protected_parts(snapshot["release"]["body"]):
        if part not in list(protected_parts(draft)):
            raise ValueError("Draft removed existing authored or artifact content")
    return draft


def compare_snapshot(snapshot, current, commit, base_commit):
    if any(snapshot["release"].get(key) != current.get(key) for key in IDENTITY):
        raise ValueError("Release changed since snapshot; refusing concurrent overwrite")
    if commit != snapshot["commit"] or base_commit != snapshot["base_commit"]:
        raise ValueError("A release-range tag moved since snapshot")


def check(args):
    snapshot = json.loads((args.trusted / "snapshot.json").read_text())
    # Read only fixed, bounded regular files after sandbox removal. Copy them
    # into trusted staging; artifact upload must never traverse model-made links.
    staged = args.trusted / "agent-output"
    staged.mkdir(exist_ok=True)
    draft = read_regular(args.output / "release-notes.md")
    (staged / "release-notes.md").write_text(draft)
    diff = "".join(difflib.unified_diff(snapshot["release"]["body"].splitlines(True), draft.splitlines(True), fromfile="original", tofile="draft"))
    (args.trusted / "release-notes.diff").write_text(diff)
    (staged / "release-notes.diff").write_text(diff)
    evidence = read_regular(args.output / "evidence.json")
    (staged / "evidence.json").write_text(evidence)
    validate(snapshot, staged)
    if snapshot["release"]["tag_name"] != args.tag:
        raise ValueError("Requested tag differs from snapshot")
    if not args.publish:
        return
    current = release(args.tag)
    compare_snapshot(snapshot, current, remote_commit(args.tag), remote_commit(snapshot["base"]))
    if current["tag_name"] != args.tag:
        raise ValueError("Publishing tag differs from snapshot")
    # Copy the validated bytes to the trusted directory. Never publish a mutable
    # agent workspace file, and never execute anything downloaded from the agent.
    final = args.trusted / "validated-notes.md"
    final.write_text(draft)
    subprocess.run(["gh", "release", "edit", args.tag, "--repo", REPO, "--notes-file", str(final)], check=True)
    after = release(args.tag)
    write_json(args.trusted / "release-after.json", after)
    if after["body"] != draft or any(after.get(k) != current.get(k) for k in IDENTITY if k not in ("body", "updated_at")):
        raise ValueError("Post-publication verification failed; do not retry automatically")
    if remote_commit(args.tag) != snapshot["commit"]:
        raise ValueError("Tag moved during publication")
    print(after["html_url"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    collect_parser = sub.add_parser("collect")
    collect_parser.add_argument("--tag", required=True, type=tag)
    collect_parser.add_argument("--base", default="")
    collect_parser.add_argument("--source", required=True, type=Path)
    collect_parser.add_argument("--output", required=True, type=Path)
    check_parser = sub.add_parser("check")
    check_parser.add_argument("--tag", required=True, type=tag)
    check_parser.add_argument("--trusted", required=True, type=Path)
    check_parser.add_argument("--output", required=True, type=Path)
    check_parser.add_argument("--publish", action="store_true")
    args = parser.parse_args()
    try:
        (collect if args.command == "collect" else check)(args)
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        print(f"Release notes failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
