#!/usr/bin/env python3
"""Live Docker Hub lookups for authoring sbxenv.yaml files.

Every subcommand queries Docker Hub and the registry at the moment it runs;
nothing is cached, because the kit catalog, tags and digests change daily.

  search   [QUERY ...] [--orgs sbx,docker] [--include-v2]
           v3 kits published under the given orgs. Each QUERY runs as a
           separate Hub search and the results are merged; no QUERY lists
           the whole catalog for those orgs.
  describe REF [REF ...]
           Resolve each kit reference and summarize its published v3
           descriptor: kind, handle, version, the args an sbxenv can set,
           the credentials it needs (with the binding they imply), and the
           host grants it asks for.
  tags     REF
           Every tag of the repository with its digest, newest first.
  update   REF [REF ...] [--pin]
           The reference each kit entry should move to: the newest release
           tag (or the same moving tag), carrying the digest when REF is
           digest-pinned or --pin is given.

Output is JSON on stdout; errors go to stderr with a nonzero exit.
Only public repositories are supported (anonymous registry access).
"""

import argparse
import json
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

HUB = "https://hub.docker.com"
DOCKER_IO = "registry-1.docker.io"
INDEX_ACCEPT = ", ".join([
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.oci.image.manifest.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
    "application/vnd.docker.distribution.manifest.v2+json",
])
DESCRIPTOR_ANNOTATION = "vnd.docker.sandbox.kit.descriptor"
V3_MEDIA_TYPE = "application/vnd.docker.sandbox.kit.v3"
CAP_PREFIX = "com.docker.sandbox/"
SEMVER = re.compile(r"^v?(\d+)\.(\d+)(?:\.(\d+))?(?:-([0-9A-Za-z.-]+))?$")


class Fail(Exception):
    pass


# --------------------------------------------------------------------------
# HTTP


def _request(url, headers=None, method="GET"):
    req = urllib.request.Request(url, headers=headers or {}, method=method)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.status, dict(resp.headers), resp.read()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read()
    except urllib.error.URLError as e:
        raise Fail(f"{url}: {e.reason}")


def get_json(url):
    status, _, body = _request(url, {"Accept": "application/json"})
    if status != 200:
        raise Fail(f"GET {url}: HTTP {status}: {body[:300].decode(errors='replace')}")
    return json.loads(body)


# --------------------------------------------------------------------------
# References


class Ref:
    """A parsed OCI reference: registry/repository[:tag][@digest]."""

    def __init__(self, text):
        self.text = text
        rest = text
        for prefix in ("docker://", "oci://"):
            if rest.startswith(prefix):
                rest = rest[len(prefix):]
        self.digest = None
        if "@" in rest:
            rest, self.digest = rest.split("@", 1)
        self.tag = None
        last = rest.rsplit("/", 1)[-1]
        if ":" in last:
            rest, self.tag = rest.rsplit(":", 1)
        parts = rest.split("/")
        if len(parts) > 1 and ("." in parts[0] or ":" in parts[0] or parts[0] == "localhost"):
            self.registry, self.repo = parts[0], "/".join(parts[1:])
        else:
            self.registry, self.repo = "docker.io", rest
        if self.registry in ("docker.io", "index.docker.io", "registry-1.docker.io"):
            self.registry = "docker.io"
            if "/" not in self.repo:
                self.repo = "library/" + self.repo
        if not self.repo:
            raise Fail(f"{text}: not an OCI reference")

    @property
    def hub(self):
        return self.registry == "docker.io"

    @property
    def api_host(self):
        return DOCKER_IO if self.hub else self.registry

    @property
    def handle(self):
        # sbx names a v3 kit after the last path segment of its repository;
        # that is what `agent:` refers to for a workload kit.
        return self.repo.rsplit("/", 1)[-1]

    def name(self):
        """The repository as an author writes it in sbxenv.yaml."""
        if self.hub:
            return self.repo[len("library/"):] if self.repo.startswith("library/") else self.repo
        return f"{self.registry}/{self.repo}"

    def format(self, tag=None, digest=None):
        out = self.name()
        if tag:
            out += ":" + tag
        if digest:
            out += "@" + digest
        return out


def is_oci_reference(text):
    """Directory, ZIP and git sources are not registry lookups."""
    if text.startswith(("./", "../", "/", "~", "git+")) or text in (".", ".."):
        return False
    if text.endswith(".zip"):
        return False
    return True


# --------------------------------------------------------------------------
# Registry access (anonymous bearer tokens)

_tokens = {}


def _token(ref):
    key = (ref.api_host, ref.repo)
    if key in _tokens:
        return _tokens[key]
    status, headers, _ = _request(f"https://{ref.api_host}/v2/")
    token = None
    challenge = next((v for k, v in headers.items() if k.lower() == "www-authenticate"), "")
    if status == 401 and challenge.lower().startswith("bearer"):
        params = dict(re.findall(r'(\w+)="([^"]*)"', challenge))
        query = {"scope": f"repository:{ref.repo}:pull"}
        if "service" in params:
            query["service"] = params["service"]
        token = get_json(params["realm"] + "?" + urllib.parse.urlencode(query)).get("token")
    _tokens[key] = token
    return token


def manifest(ref, reference, method="GET"):
    """Return (digest, manifest-json-or-None) for a tag or digest."""
    headers = {"Accept": INDEX_ACCEPT}
    token = _token(ref)
    if token:
        headers["Authorization"] = "Bearer " + token
    url = f"https://{ref.api_host}/v2/{ref.repo}/manifests/{reference}"
    status, resp_headers, body = _request(url, headers, method)
    if status == 401 or status == 403:
        raise Fail(f"{ref.format(reference)}: access denied (private repository? only public kits are supported)")
    if status == 404:
        raise Fail(f"{ref.format(reference)}: not found")
    if status != 200:
        raise Fail(f"{ref.format(reference)}: HTTP {status}")
    digest = next((v for k, v in resp_headers.items() if k.lower() == "docker-content-digest"), None)
    return digest, (json.loads(body) if method == "GET" else None)


def list_tags(ref):
    """Tags newest first, as [{tag, digest, updated}]."""
    if ref.hub:
        ns, repo = ref.repo.split("/", 1)
        url = f"{HUB}/v2/namespaces/{ns}/repositories/{repo}/tags?page_size=100&ordering=last_updated"
        out = []
        while url:
            status, _, body = _request(url, {"Accept": "application/json"})
            if status == 404:
                raise Fail(f"{ref.name()}: repository not found or private (only public kits are supported)")
            if status != 200:
                raise Fail(f"{ref.name()}: listing tags failed with HTTP {status}")
            page = json.loads(body)
            for t in page.get("results", []):
                out.append({"tag": t["name"], "digest": t.get("digest"), "updated": t.get("last_updated")})
            url = page.get("next")
        return out
    headers = {"Accept": "application/json"}
    token = _token(ref)
    if token:
        headers["Authorization"] = "Bearer " + token
    status, _, body = _request(f"https://{ref.api_host}/v2/{ref.repo}/tags/list", headers)
    if status != 200:
        raise Fail(f"{ref.name()}: listing tags failed with HTTP {status}")
    return [{"tag": t, "digest": None, "updated": None} for t in json.loads(body).get("tags") or []]


# --------------------------------------------------------------------------
# Descriptor summary


def _creds(cap):
    cfg = cap.get("config") or {}
    service = cfg.get("service")
    api = cfg.get("apiKey") or {}
    oauth = cfg.get("oauth") or {}
    api_domains = sorted({i["domain"] for i in api.get("inject") or [] if i.get("domain")})
    oauth_domains = []
    if oauth:
        host = (oauth.get("tokenEndpoint") or {}).get("host")
        oauth_domains = sorted({d for d in [host, *(oauth.get("resourceHosts") or [])] if d})
    binding = {}
    if api_domains:
        binding["apiKey"] = {"domains": api_domains}
    if oauth_domains:
        binding["oauth"] = {"domains": oauth_domains}
    return {
        "service": service,
        "name": cap.get("name"),
        "phase": cfg.get("phase"),
        "optional": bool(cap.get("optional")),
        "apiKeyEnv": api.get("name"),
        "mechanisms": sorted(binding),
        "suggestedBinding": {service: binding} if service and binding else None,
    }


def summarize(ref, digest, man):
    annotations = (man or {}).get("annotations") or {}
    raw = annotations.get(DESCRIPTOR_ANNOTATION)
    if not raw:
        kind = (man or {}).get("artifactType") or annotations.get("vnd.docker.sandbox.kit.schema-version")
        return {
            "ref": ref.text,
            "digest": digest,
            "v3": False,
            "note": "no v3 descriptor annotation; this is not a published v3 kit "
                    f"(artifactType: {kind or 'none'})",
        }
    d = json.loads(raw)
    args, baked = {}, {}
    for name, decl in (d.get("args") or {}).items():
        if decl.get("buildArg"):
            baked[name] = decl.get("default")
            continue
        args[name] = {k: decl[k] for k in ("description", "default", "required", "enum", "pattern", "env") if k in decl}
    creds, grants, types = [], [], []
    for cap in d.get("capabilities") or []:
        t = cap.get("type", "")
        short = t[len(CAP_PREFIX):] if t.startswith(CAP_PREFIX) else t
        types.append(short)
        if short.startswith("credential@"):
            creds.append(_creds(cap))
        elif short.split("@")[0] in ("ssh-agent", "host-port", "host-mount", "privileged", "dockerd",
                                    "git-identity", "usb-device", "port", "resources", "long-running"):
            grants.append({"type": short, "optional": bool(cap.get("optional")), "config": cap.get("config")})
    provides = [p for p in d.get("provides") or [] if not p.startswith(("deb/", "apk/", "rpm/"))]
    return {
        "ref": ref.text,
        "digest": digest,
        "v3": True,
        "handle": ref.handle,
        "kind": d.get("kind"),
        "agent": "sbx@1" in types,
        "displayName": d.get("displayName"),
        "description": d.get("description"),
        "version": d.get("version") or baked.get("version") or annotations.get("org.opencontainers.image.version"),
        "sourceUrl": d.get("sourceUrl"),
        "args": args,
        "bakedArgs": baked,
        "credentials": creds,
        "hostGrants": grants,
        "capabilities": sorted(set(types)),
        "provides": provides,
        "requires": d.get("requires") or [],
    }


def describe_one(text):
    ref = Ref(text)
    target = ref.digest or ref.tag or "latest"
    digest, man = manifest(ref, target)
    if ref.digest and digest and digest != ref.digest:
        raise Fail(f"{text}: registry returned digest {digest}")
    if DESCRIPTOR_ANNOTATION not in (man.get("annotations") or {}):
        # Earlier v3 releases annotate each platform manifest, not the index.
        platform = next((m for m in man.get("manifests") or []
                         if (m.get("platform") or {}).get("os") not in (None, "unknown")), None)
        if platform:
            _, child = manifest(ref, platform["digest"])
            if DESCRIPTOR_ANNOTATION in (child.get("annotations") or {}):
                man = child
    return summarize(ref, digest or ref.digest, man)


# --------------------------------------------------------------------------
# Updates


def _semver(tag):
    m = SEMVER.match(tag)
    if not m:
        return None
    major, minor, patch, pre = m.groups()
    return (int(major), int(minor), int(patch or 0), pre)


def _sort_key(v):
    major, minor, patch, pre = v
    # A release sorts after its prereleases.
    return (major, minor, patch, pre is None, pre or "")


def _newest_release(tags, like=None):
    """The highest semver tag, matching the prerelease and v-prefix style of like."""
    allow_pre = bool(like and like[1][3] is not None)
    prefixed = bool(like and like[0].startswith("v"))
    candidates = [(t["tag"], _semver(t["tag"])) for t in tags]
    candidates = [(t, v) for t, v in candidates if v and (allow_pre or v[3] is None)]
    candidates = [c for c in candidates if c[0].startswith("v") == prefixed] or candidates
    return max(candidates, key=lambda c: _sort_key(c[1]), default=None)


def update_one(text, pin):
    ref = Ref(text)
    cur_v = _semver(ref.tag) if ref.tag else None
    pinned = bool(ref.digest) or pin
    newest, note = None, None

    if ref.tag and not cur_v:
        # A moving tag (latest, edge, ...): follow it, and name the newest
        # release in case the author would rather move to a version.
        target = ref.tag
        note = f"'{ref.tag}' is a moving tag; only its digest changes"
        try:
            newest = _newest_release(list_tags(ref))
        except Fail:
            pass
    elif ref.tag or not ref.digest:
        tags = list_tags(ref)
        newest = _newest_release(tags, (ref.tag, cur_v) if cur_v else None)
        if cur_v:
            target = newest[0] if newest and _sort_key(newest[1]) > _sort_key(cur_v) else ref.tag
            if newest and newest[1][0] > cur_v[0]:
                note = f"major version change {ref.tag} -> {target}; check the kit's args and capabilities"
        elif newest:
            target = newest[0]
            note = "the reference names no tag (implicitly latest); suggesting the newest release tag"
        else:
            target = "latest"
            note = "no release tags; following latest"
    else:
        target = None
        note = "digest-only reference; no tag to follow"

    new_digest = manifest(ref, target, method="HEAD")[0] if target else ref.digest
    suggested = ref.format(target, new_digest if pinned else None)
    return {
        "ref": text,
        "currentTag": ref.tag,
        "currentDigest": ref.digest,
        "latestTag": target,
        "latestDigest": new_digest,
        "newestRelease": newest[0] if newest else None,
        "pinned": pinned,
        "suggested": suggested,
        "changed": suggested != ref.format(ref.tag, ref.digest),
        "note": note,
    }


# --------------------------------------------------------------------------
# Search


def search(queries, orgs, include_v2):
    seen, out = set(), []
    for q in queries or [""]:
        start, total = 0, None
        while total is None or start < total:
            params = urllib.parse.urlencode({"query": q, "type": "sbx_kit", "from": start, "size": 100})
            page = get_json(f"{HUB}/api/search/v4?{params}")
            results = page.get("results") or []
            total = page.get("total", 0)
            if not results:
                break
            start += len(results)
            for r in results:
                slug = r.get("slug") or r.get("id") or ""
                if slug in seen or slug.split("/", 1)[0] not in orgs:
                    continue
                media = r.get("media_types") or []
                v3 = V3_MEDIA_TYPE in media
                if not v3 and not include_v2:
                    continue
                seen.add(slug)
                out.append({
                    "ref": slug,
                    "handle": slug.rsplit("/", 1)[-1],
                    "name": r.get("name"),
                    "description": r.get("short_description"),
                    "v3": v3,
                    "pulls": r.get("raw_pull_count"),
                    "updated": r.get("updated_at"),
                    "matched": q or None,
                })
    out.sort(key=lambda r: -(r["pulls"] or 0))
    return out


# --------------------------------------------------------------------------


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("search")
    s.add_argument("queries", nargs="*")
    s.add_argument("--orgs", default="sbx,docker", help="comma-separated Hub namespaces (default: sbx,docker)")
    s.add_argument("--include-v2", action="store_true", help="also list v2 kits")
    d = sub.add_parser("describe")
    d.add_argument("refs", nargs="+")
    t = sub.add_parser("tags")
    t.add_argument("ref")
    u = sub.add_parser("update")
    u.add_argument("refs", nargs="+")
    u.add_argument("--pin", action="store_true", help="pin digests even for refs that are not pinned yet")
    a = p.parse_args()

    try:
        if a.cmd == "search":
            result = search(a.queries, {o.strip() for o in a.orgs.split(",") if o.strip()}, a.include_v2)
        elif a.cmd == "tags":
            result = list_tags(Ref(a.ref))
        else:
            fn = describe_one if a.cmd == "describe" else (lambda r: update_one(r, a.pin))
            result = []
            for r in a.refs:
                if not is_oci_reference(r):
                    result.append({"ref": r, "skipped": "local or git source; not a registry reference"})
                    continue
                try:
                    result.append(fn(r))
                except Fail as e:
                    result.append({"ref": r, "error": str(e)})
    except Fail as e:
        print(f"error: {e}", file=sys.stderr)
        return 1
    json.dump(result, sys.stdout, indent=2)
    print()
    return 0


if __name__ == "__main__":
    sys.exit(main())
