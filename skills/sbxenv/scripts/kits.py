#!/usr/bin/env python3
"""Live Docker Hub lookups for authoring sbxenv.yaml files.

Every subcommand queries Docker Hub and the registry at the moment it runs;
nothing is cached, because the kit catalog, tags and digests change daily.

  search   [QUERY ...] [--orgs sbx,docker] [--include-v2]
           v3 kits published under the given orgs. Each QUERY runs as a
           separate Hub search and the results are merged; no QUERY lists
           the whole catalog for those orgs.
  describe REF [REF ...] [--arg [HANDLE.]NAME=VALUE ...]
           Resolve each kit reference and summarize its published v3
           descriptor: kind, handle, version, the args an sbxenv can set,
           the credentials it needs (with the binding they imply), and the
           host grants it asks for, including those inside capability
           groups. Create-arg references in a credential resolve from the
           arg defaults and any --arg (HANDLE. scopes it to one kit, as
           sbx's --kit-arg does); one that cannot resolve is listed under
           `unresolved` and suggests no binding.
  tags     REF
           Every tag of the repository with its digest, newest first.
  update   REF [REF ...] [--pin]
           The reference each kit entry should move to: the newest release
           tag (or the same moving tag), carrying the digest when REF is
           digest-pinned or --pin is given. A digest-only REF moves to the
           newest release's digest and stays digest-only.

Output is JSON on stdout. describe and update report a failed lookup in
that reference's entry, keep the other results, and exit 1 if any lookup
failed. Only public repositories are supported (anonymous registry access).
Descriptors published as YAML (earlier v3 kits) need PyYAML installed;
current kits publish JSON and need nothing beyond the standard library.
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
# A registry index nests at most this deep before the walk gives up; the
# fetcher in fetch/ bounds the same walk the same way.
MAX_INDEX_DEPTH = 4
MAX_TAG_PAGES = 100
KIT_REF = re.compile(r"\$\{\{\s*kit\.(args|env)\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}")
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
    origin = f"https://{ref.api_host}"
    url, out = f"{origin}/v2/{ref.repo}/tags/list", []
    for _ in range(MAX_TAG_PAGES):
        status, resp_headers, body = _request(url, headers)
        if status != 200:
            raise Fail(f"{ref.name()}: listing tags failed with HTTP {status}")
        out += [{"tag": t, "digest": None, "updated": None} for t in json.loads(body).get("tags") or []]
        link = next((v for k, v in resp_headers.items() if k.lower() == "link"), "")
        m = re.search(r'<([^>]+)>\s*;\s*rel="?next"?', link)
        if not m:
            return out
        url = urllib.parse.urljoin(url, m.group(1))
        # The bearer token belongs to this registry: never send it elsewhere.
        if not url.startswith(origin + "/"):
            raise Fail(f"{ref.name()}: tag listing points to another origin ({url}); refusing to follow it")
    raise Fail(f"{ref.name()}: tag listing exceeds {MAX_TAG_PAGES} pages")


# --------------------------------------------------------------------------
# Descriptor summary


def _expand(value, values, unresolved):
    """Substitute ${{ kit.args.NAME }} from values. Anything left over (an
    arg without a value, or ${{ kit.env.NAME }}, which only the container
    environment at create can answer) is recorded and stays in the text."""
    def sub(m):
        if m.group(1) == "args" and m.group(2) in values:
            return values[m.group(2)]
        unresolved.add(m.group(0).replace(" ", ""))
        return m.group(0)
    return KIT_REF.sub(sub, value) if isinstance(value, str) else value


def _creds(cap, optional, group, values):
    cfg = cap.get("config") or {}
    unresolved = set()
    service = _expand(cfg.get("service"), values, unresolved)
    # A mechanism is declared by its block being present, not by it naming
    # domains: an apiKey with only `name` (an env var, no inject rules) is
    # valid, and still needs a binding before the runtime will supply it.
    api = cfg.get("apiKey")
    oauth = cfg.get("oauth")
    binding = {}
    if api is not None:
        domains = [i.get("domain") for i in api.get("inject") or []]
        binding["apiKey"] = {"domains": sorted({_expand(d, values, unresolved) for d in domains if d})}
    if oauth is not None:
        host = (oauth.get("tokenEndpoint") or {}).get("host")
        domains = [host, *(oauth.get("resourceHosts") or [])]
        binding["oauth"] = {"domains": sorted({_expand(d, values, unresolved) for d in domains if d})}
    return {
        "service": service,
        "name": cap.get("name") or group,
        "phase": cfg.get("phase"),
        "optional": optional,
        "apiKeyEnv": (api or {}).get("name"),
        "mechanisms": sorted(binding),
        # A binding holding a literal ${{ ... }} would be copied into an
        # sbxenv, where kit references mean nothing; withhold it instead.
        "suggestedBinding": {service: binding} if service and binding and not unresolved else None,
        "unresolved": sorted(unresolved),
    }


def _decode_descriptor(raw):
    """Published descriptors are compact JSON; earlier kits published YAML,
    which SPEC-v3 §9.3 keeps readable. JSON is a subset of YAML, so a YAML
    parser reads both, but the standard library only has the JSON one."""
    try:
        return json.loads(raw)
    except ValueError:
        pass
    try:
        import yaml
    except ImportError:
        raise Fail("descriptor annotation is YAML (a kit published before descriptors "
                   "switched to JSON); install PyYAML to read it")
    try:
        return yaml.safe_load(raw)
    except yaml.YAMLError as e:
        raise Fail(f"descriptor annotation does not decode: {e}")


def _capabilities(d):
    """Yield (capability, optional, group name) for every request, including
    the members of capability groups (SPEC-v3 §7.1.1). A member carries no
    optionality of its own; it takes its group's."""
    for item in d.get("capabilities") or []:
        group = item.get("group")
        if group is None:
            yield item, bool(item.get("optional")), None
            continue
        for member in group.get("capabilities") or []:
            yield member, bool(group.get("optional")), group.get("name")


def summarize(ref, digest, man, scoped=None, unscoped=None):
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
    d = _decode_descriptor(raw)
    args, build_args = {}, {}
    for name, decl in (d.get("args") or {}).items():
        if decl.get("buildArg"):
            # Fixed when the kit was published, so an sbxenv cannot set it.
            # The descriptor keeps the declared default, not the value the
            # publisher built with (a build can override it), so this is no
            # evidence of what the kit contains; `version` is.
            build_args[name] = {k: decl[k] for k in ("description", "default") if k in decl}
            continue
        args[name] = {k: decl[k] for k in ("description", "default", "required", "enum", "pattern", "env") if k in decl}
    # Unscoped values apply to every kit that declares the name; a value
    # scoped to this kit must name an arg it declares, as with --kit-arg.
    values = {n: a["default"] for n, a in args.items() if "default" in a}
    values.update({n: v for n, v in (unscoped or {}).items() if n in args})
    values.update(scoped or {})
    unknown = sorted(set(scoped or {}) - set(args))
    if unknown:
        raise Fail(f"{ref.text}: no create-phase arg named {', '.join(unknown)}")
    creds, grants, types = [], [], []
    for cap, optional, group in _capabilities(d):
        t = cap.get("type", "")
        short = t[len(CAP_PREFIX):] if t.startswith(CAP_PREFIX) else t
        types.append(short)
        if short.startswith("credential@"):
            creds.append(_creds(cap, optional, group, values))
        elif short.split("@")[0] in ("ssh-agent", "host-port", "host-mount", "privileged", "dockerd",
                                    "git-identity", "usb-device", "port", "resources", "long-running"):
            grants.append({"type": short, "optional": optional, "group": group, "config": cap.get("config")})
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
        # The published descriptor is the expanded document, so its version
        # is the built one. Without it, the frontend derives the OCI version
        # annotation from the provides entries when they agree.
        "version": d.get("version") or annotations.get("org.opencontainers.image.version"),
        "sourceUrl": d.get("sourceUrl"),
        "args": args,
        "buildArgs": build_args,
        "credentials": creds,
        "hostGrants": grants,
        "capabilities": sorted(set(types)),
        "provides": provides,
        "requires": d.get("requires") or [],
    }


def _is_index(media_type):
    return media_type in ("application/vnd.oci.image.index.v1+json",
                          "application/vnd.docker.distribution.manifest.list.v2+json")


def _find_descriptor(ref, man, depth=0):
    """The manifest carrying the descriptor annotation, or None.

    The frontend promotes the annotation onto the index only when the
    export shape allows it; the platform manifest always carries it
    (SPEC-v3 §9.3). Every platform carries the same descriptor, so the
    first image manifest that has it will do. An image manifest marked
    os=unknown is an attestation and is skipped; a nested index is
    followed whatever its platform says, because os=unknown is also how
    an index with no platform of its own is published.
    """
    if DESCRIPTOR_ANNOTATION in (man.get("annotations") or {}):
        return man
    if depth >= MAX_INDEX_DEPTH or not _is_index(man.get("mediaType", "")) and "manifests" not in man:
        return None
    children = man.get("manifests") or []
    images = [m for m in children if not _is_index(m.get("mediaType", ""))
              and (m.get("platform") or {}).get("os") != "unknown"]
    nested = [m for m in children if _is_index(m.get("mediaType", ""))]
    for m in images + nested:
        _, child = manifest(ref, m["digest"])
        found = _find_descriptor(ref, child, depth + 1)
        if found:
            return found
    return None


def describe_one(text, scoped=None, unscoped=None):
    ref = Ref(text)
    target = ref.digest or ref.tag or "latest"
    digest, man = manifest(ref, target)
    if ref.digest and digest and digest != ref.digest:
        raise Fail(f"{text}: registry returned digest {digest}")
    return summarize(ref, digest or ref.digest, _find_descriptor(ref, man) or man, scoped, unscoped)


# --------------------------------------------------------------------------
# Updates


def _semver(tag):
    m = SEMVER.match(tag)
    if not m:
        return None
    major, minor, patch, pre = m.groups()
    return (int(major), int(minor), int(patch or 0), pre)


def _sort_key(v):
    """SemVer precedence: a release sorts after its prereleases, and
    prerelease identifiers compare one by one, numeric ones numerically and
    below alphanumeric ones, with a shorter run of equal identifiers first."""
    major, minor, patch, pre = v
    ids = tuple((0, int(i), "") if i.isdigit() else (1, 0, i) for i in pre.split(".")) if pre else ()
    return (major, minor, patch, pre is None, ids)


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
        # Digest-only: follow the newest release (or latest when there are
        # none), but keep the file's spelling, a bare digest.
        newest = _newest_release(list_tags(ref))
        target = newest[0] if newest else "latest"
        note = (f"digest-only reference; resolved the newest release ({target}) and kept the digest-only form"
                if newest else "digest-only reference with no release tags; resolved latest")

    new_digest = manifest(ref, target, method="HEAD")[0]
    tag_out = None if (ref.digest and not ref.tag) else target
    suggested = ref.format(tag_out, new_digest if pinned else None)
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
    d.add_argument("--arg", action="append", default=[], metavar="[HANDLE.]NAME=VALUE",
                   help="create-phase arg value for resolving credentials (repeatable)")
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
            if a.cmd == "describe":
                pairs = []
                for item in a.arg:
                    name, sep, value = item.partition("=")
                    if not sep or not name:
                        raise Fail(f"--arg {item!r}: expected [HANDLE.]NAME=VALUE")
                    pairs.append((*name.rpartition(".")[::2], value))

                def fn(r):
                    handle = Ref(r).handle
                    return describe_one(r, {n: v for h, n, v in pairs if h == handle},
                                        {n: v for h, n, v in pairs if h == ""})
            else:
                def fn(r):
                    return update_one(r, a.pin)
            result = []
            for r in a.refs:
                if not is_oci_reference(r):
                    result.append({"ref": r, "skipped": "local or git source; not a registry reference"})
                    continue
                try:
                    result.append(fn(r))
                except Fail as e:
                    result.append({"ref": r, "error": str(e)})
                except Exception as e:
                    # One malformed kit must not cost the rest of the batch.
                    result.append({"ref": r, "error": f"{type(e).__name__}: {e}"})
    except Fail as e:
        print(f"error: {e}", file=sys.stderr)
        return 1
    json.dump(result, sys.stdout, indent=2)
    print()
    failed = isinstance(result, list) and any(isinstance(e, dict) and "error" in e for e in result)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
