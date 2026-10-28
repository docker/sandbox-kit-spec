# Content recipes

Patterns for the `<kit>.dockerfile` beside a descriptor, and the invariants an
overlay has to satisfy. Reference for [SKILL.md](SKILL.md).

## A workload

A workload's layers are the root filesystem, so its recipe is an ordinary
image build. Build on a base carrying the runtime's platform floor — `bash`,
`sh`, `curl`, `git`, a CA store, and a non-root `agent` user at uid 1000 with
home `/home/agent` — which the hardened `dhi.io/sbx-templates:*` images
provide. A bare distro base builds fine and fails at agent launch.

```dockerfile
# syntax=docker/dockerfile:1
ARG BASE_IMAGE=dhi.io/sbx-templates:shell-docker
FROM ${BASE_IMAGE}

ARG TOOL_VERSION
USER root
RUN set -eux; \
    curl -fsSL "https://example.com/releases/${TOOL_VERSION}/tool-linux-$(dpkg --print-architecture)" \
      -o /usr/local/bin/tool; \
    chmod 0755 /usr/local/bin/tool; \
    # The pin is a claim about content: make the build enforce it.
    tool --version | grep -q "${TOOL_VERSION}"

# The runtime contract lives in the image config, not the descriptor.
ENV IS_SANDBOX=1
USER agent
WORKDIR /home/agent/workspace
ENTRYPOINT ["tool", "--dangerously-skip-permissions"]
```

`ENTRYPOINT` plus `CMD` is the headless argv. Use `CMD` only for default
*arguments* to the entrypoint's binary; a `CMD` that repeats the binary
appends a stray argument. An interactive argv tail has no image-config slot and
goes in `lifecycle@1`'s `interactive:` field.

## A mixin overlay

An overlay lands on a filesystem you have never seen. Stage everything under
`/out` in a build stage and copy that into `scratch`:

```dockerfile
# syntax=docker/dockerfile:1
FROM dhi.io/debian-base:trixie-dev AS build
ARG TOOL_VERSION
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates
RUN set -eux; \
    mkdir -p /out/usr/local/bin; \
    curl -fsSL "https://example.com/releases/${TOOL_VERSION}/tool" -o /out/usr/local/bin/tool; \
    chmod 0755 /out/usr/local/bin/tool

# The overlay: lands on any base.
FROM scratch
COPY --from=build /out /

# Static env belongs on the FINAL stage — a build stage's config is discarded.
# A mixin's additive config merges, so this reaches the composed image and the
# agent process; an /etc/profile.d drop would only reach a login shell.
ENV TOOL_HOME=/opt/tool
```

A merged env key other than `PATH` is first-writer-owned: a second kit may
restate the same value, and a different one refuses the composition. Keep
`ENV` to keys the tool owns. A key a base may already set to something else —
`BROWSER`, `TERM`, `LANG` — is what the `codex-mixin` and `cursor-mixin`
examples leave to a login profile instead, which applies over the base's value
and reaches only login shells.

Prefer `/usr/local` and `/opt`, and **avoid the agent's home
entirely** where you can — whatever is at `/home/agent` on the composed base
may be a mounted volume, and staging there is how the ownership traps below
get hit.

Where the install cannot be relocated — an installer with no prefix option, a
language toolchain that bakes absolute paths — run the unmodified install on
the workload's own base as the build stage, then copy the specific resulting
paths out. Say in a comment why that shape was chosen. **Not apt packages**:
copying selected paths out of one leaves its dpkg state and shared-library
closure behind, as the next paragraph says, so the copy-out shape produces a
broken overlay rather than a portable one.

Some things genuinely cannot travel in an overlay, and the honest move is to
document the limitation rather than fake it: apt packages (they need the
composed base's dpkg database and their shared-library closure), and anything
that must *merge* into a file the base also writes, since a layer replaces a
file rather than merging it.

**A virtualenv travels only if its interpreter travels with it.** A venv keeps
packages in `lib/python3.<minor>` and points `bin/python` at an absolute
interpreter path. Point it at the build base's `/usr/bin/python3` and the
copied tree resolves the *composed* base's python instead, looks for
site-packages under that base's minor version, and fails with
`ModuleNotFoundError` — after the launcher has started, so the kit reports
success at create and breaks at use. Two ways out, and the choice is the whole
decision:

- **Bundle the interpreter.** `uv tool install --managed-python --python 3.14`
  downloads a standalone CPython into `~/.local/share/uv`, which the overlay
  copies, so `bin/python` resolves inside the tree it ships with. Verify by
  composing onto a base with **no python at all** and running the tool; if it
  works there, it works anywhere.
- **Leave it a create-time hook.** Where the venv must use the base's own
  python — because an apt hook installs that python in the first place — the
  install belongs at create, and the overlay carries only what is portable
  (an `ENV` and a wrapper script, say).

The failure is invisible when the build base and the test base happen to share
a minor version, so check `readlink -f <venv>/bin/python`: a path under the
overlay's own tree is self-contained, `/usr/bin/python3` is not.

## Ownership

**An overlay's directory entries override the base's**, so an overlay states
ownership for every level it ships, and both directions are bugs:

- `/home` owned by uid 1000 hands the agent a directory it should not own.
- `/home/agent` owned by root takes `$HOME` from the agent user — and the
  entrypoint runs as that user, so its own `chown` would be a no-op.

Three ways to get this wrong, all seen in practice:

```dockerfile
COPY --chown=1000:1000 x /home/agent/x   # BuildKit chowns every parent it creates → /home is 1000
RUN chown -R 1000:1000 /out              # same result, via the staging root
RUN chown -R 1000:1000 /out/home/agent/x # one level too deep → /home/agent stays root
```

The idiom that gets both right starts the chown exactly at the agent's home:

```dockerfile
USER root
RUN mkdir -p /out/home/agent \
 && cp -a /home/agent/.local /out/home/agent/.local \
 && chown -R 1000:1000 /out/home/agent
```

Numeric ids, because `scratch` carries no `/etc/passwd` for a name to resolve
against.

**Foreign owners ride along in package trees.** npm and PyPI tarballs and
vendor release archives preserve whatever uid the publisher's machine had, and
`cp -a` or `tar -x` carries it in — real examples are `501:20` (a macOS
developer), `1001:1001` and `2000:2000` (CI runners). Harmless in a build
stage; as image content on an unknown base those ids may be real accounts, and
a file's owner can rewrite it whatever its mode says. Normalize a copied
package tree with `chown -R 0:0`, which is what a root-run install leaves
anyway — the agent needs write access to a *prefix directory* to add packages,
not to the tool's own tree.

## Verifying an overlay

A build proves the recipe ran. It does not prove the overlay works.

Select the named `kit-builder` container builder created in
[Build, run, verify](SKILL.md#build-run-verify).

**Judge the exported layout with `kit-tck`:**

```sh
docker buildx build --builder kit-builder . -f <kit>.yaml -t <kit>:<version> \
  --output type=oci,dest=/tmp/layout,tar=false
kit-tck validate --layout /tmp/layout <version>
```

`kit-tck` judges the two directory levels on every platform manifest:
`overlay-home-ownership` fails an overlay that ships `/home` owned by anyone
but root or `/home/agent` by anyone but uid 1000 — `/home` handed to the agent,
or `/home/agent` taken from it. An overlay shipping neither directory passes,
because it is staying out of the home entirely. The same run warns under
`overlay-links-resolve` for every symlink the overlay's own layers do not
resolve; see the composition shortcut below for why that is a warning.

What it does not judge is **every other owner**. Count those out of the layer:

```sh
for b in /tmp/layout/blobs/sha256/*; do tar --numeric-owner -tvf "$b" 2>/dev/null; done \
  | awk '{print ($2 ~ /\//) ? $2 : $3"/"$4}' | sort | uniq -c | sort -rn
```

Every count should be `0/0` or `1000/1000`. It collapses the listing to owners,
so it finds a foreign uid and nothing else — which is why the directory levels
are `kit-tck`'s job and not this pipeline's.

**The awk is doing real work, so do not simplify it.** GNU tar prints owner
and group joined in field 2 (`0/0`); bsdtar splits them across fields 3 and 4.
A pipeline written for one prints the other's size and date — on Linux, a bare
`$3":"$4` reports `0:2026-09-21` for every entry, matches none of the values
above, and reads as a clean audit while checking nothing. `--numeric-owner`
matters too: without it a uid that resolves to a name in the build image is
reported by name, and the foreign uids this catches are exactly the ones that
do not resolve.

**Compose it for real and run the tool.** The assembler is the only thing that
performs an actual composition, so it is the check that counts:

```sh
sbx run ./<workload> --kit ./<kit> --detached --name t .
sbx exec t tool --version
```

`sbx exec` is the verb that runs a command. Arguments after `--` on `sbx run`
are **agent** arguments appended to the agent's start command, so
`sbx run … -- tool --version` starts the workload's agent with `tool --version`
tacked on and never executes the overlay's binary at all.

That merges the additive image config — the `ENV` and `PATH` the overlay sets
on its final stage — and puts the overlay on a base that carries the §12
platform floor, which is what the kit is entitled to assume.

There is a faster filesystem-only shortcut, but know what it does **not** do
before trusting it:

```sh
docker buildx build --builder kit-builder . -f <kit>.yaml -t <kit>-test:local --load
printf 'FROM <the workload base>\nCOPY --from=<kit>-test:local / /\n' \
  | docker build -t compose-test -
docker run --rm --user 1000:1000 compose-test sh -lc 'tool --version'
```

`COPY --from` transfers the overlay's **files and nothing else** — its image
config is discarded, so the `ENV` and `PATH` entries the assembler would have
merged are simply absent. A correct mixin whose tool depends on either will
fail here, and that failure is an artifact of the shortcut rather than a defect
in the kit. Export those variables by hand if you use it. Use the *workload's*
base rather than a stock `ubuntu:24.04` for the same class of reason: a bare
distro image lacks parts of the platform floor, so a kit that legitimately
relies on `git` or the `agent` user fails for reasons the real composition
would not produce.

The Dockerfile arrives on **stdin** so the build context stays empty —
pointing it at `/tmp` uploads whatever else is sitting there, including the
layout you just exported, to build two lines. And `sh -lc` sources any
`/etc/profile.d` drop the overlay ships, which a bare `sh -c` does not.

What the shortcut is genuinely good for is **dangling symlinks** against a real
base. Installers routinely relocate a launcher without its payload — a
`--prefix` or `INSTALL_DIR` option moves the symlink while the real tree stays
in `$TOOL_HOME` or a `downloads/` directory — and a build-stage `test -x` on the
launcher passes because the payload is still sitting behind it *in that stage*.
The overlay then ships a link to nothing. `kit-tck`'s `overlay-links-resolve`
warns about every link the overlay cannot resolve by itself, but only warns:
from the artifact alone, a link into the base looks the same as one into a
vanished build stage. The composition tells the two apart. Gate on the resolved
binary, and confirm with the real composition above.
