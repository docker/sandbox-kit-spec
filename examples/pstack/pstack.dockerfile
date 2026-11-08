# syntax=docker/dockerfile:1
# Text and scripts are platform-independent; download on the builder's CPU.
FROM --platform=$BUILDPLATFORM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS build
USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*

ARG PSTACK_VERSION
# The standalone mirror has no release tags. Pin both the source commit
# and archive bytes: the plugin version alone does not identify its edits.
RUN <<'SH'
set -eu
: "${PSTACK_VERSION:?PSTACK_VERSION must be supplied by the kit descriptor}"
curl -fsSL --retry 3 \
  https://codeload.github.com/backnotprop/pstack/tar.gz/a6342ded43a2437e3c664b46c2cff103787773fd \
  -o /tmp/pstack.tar.gz
sha256sum -c - <<'CHECKSUM'
476413f9fa1a7d768943b73633aa17e1aa4ee04f28d6f9ee25bad01f449e0f87  /tmp/pstack.tar.gz
CHECKSUM
mkdir -p /out/usr/share/pstack
tar -xzf /tmp/pstack.tar.gz --strip-components=1 \
  --no-same-owner --no-same-permissions -C /out/usr/share/pstack
cd /out/usr/share/pstack
grep -F "\"version\": \"$PSTACK_VERSION\"" plugin.json
grep -F "\"version\": \"$PSTACK_VERSION\"" .cursor-plugin/plugin.json
# Preserve the whole tree: skills link to siblings, agents, and docs.
for skill in skills/*; do
  test -s "$skill/SKILL.md"
  test ! -L "$skill/SKILL.md"
  # Runtimes may copy discovery bundles. Keep cross-tree references in
  # the original location, independently of optional context guidance.
  wrapper="/out/usr/share/pstack-skills/${skill##*/}"
  mkdir -p "$wrapper"
  awk '
    NR == 1 { if ($0 != "---") exit 1; print; next }
    { print }
    $0 == "---" { closed = 1; exit }
    END { if (!closed) exit 1 }
  ' "$skill/SKILL.md" > "$wrapper/SKILL.md"
  cat >> "$wrapper/SKILL.md" <<EOF

Read /usr/share/pstack/$skill/SKILL.md in full before using this skill.
Follow its instructions from that source location and resolve relative
paths there. The complete pstack tree, including sibling skills, guides,
agent templates, and supporting scripts, is bundled at /usr/share/pstack/.
EOF
done
test -s LICENSE
test -s README.md
test -s agents/poteto-agent.md
test -s agents/comment-sicko.md
test -s docs/guide/README.md
chmod -R a+rX /out/usr/share/pstack /out/usr/share/pstack-skills
chown -R 0:0 /out
SH

FROM scratch
COPY --from=build /out /
