# syntax=docker/dockerfile:1
# Skills are text; run the installer natively for either output platform.
FROM --platform=$BUILDPLATFORM dhi.io/node:24-debian13-dev AS build
USER root

# The installer is build-only; the overlay carries neither Node nor npm.
RUN npm install -g --prefix /opt/skills-hub --no-fund --no-audit \
      @skills-hub-ai/cli@0.4.4 \
 && /opt/skills-hub/bin/skills-hub --version | grep -Fx 0.4.4

ARG SKILL
ARG SKILL_VERSION
WORKDIR /tmp/skill-build
RUN <<'SH'
set -eu
# Node's fetch must use the builder's proxy just as npm does.
export NODE_USE_ENV_PROXY=1
cli=/opt/skills-hub/bin/skills-hub
"$cli" info "$SKILL" --json > skill.json
node <<'JS'
const fs = require('node:fs');
const skill = JSON.parse(fs.readFileSync('skill.json', 'utf8'));
if (skill.slug !== process.env.SKILL) {
  throw new Error('The registry returned a different skill slug');
}
// One declaration registers one skill. Dependencies need their own Kits.
if (skill.isComposition || skill.composition?.children?.length) {
  throw new Error('Choose an individual skill; compositions need one Kit per child');
}
JS
# The CLI's global --version shadows install's long flag in 0.4.4.
"$cli" install "$SKILL" -v "$SKILL_VERSION" --target claude-code --no-deps
node <<'JS'
const fs = require('node:fs');
const manifest = JSON.parse(fs.readFileSync('.skills.json', 'utf8'));
if (manifest.skills[process.env.SKILL]?.version !== process.env.SKILL_VERSION) {
  throw new Error('The installed skill version differs from the Kit version');
}
const { createRequire } = require('node:module');
const yaml = createRequire('/opt/skills-hub/lib/node_modules/@skills-hub-ai/cli/package.json')('yaml');
const path = require('node:path').join(require('node:os').homedir(), '.claude', 'skills', process.env.SKILL, 'SKILL.md');
const content = fs.readFileSync(path, 'utf8');
const frontmatter = /^---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|$)/.exec(content);
if (!frontmatter) throw new Error('The installed skill has no YAML frontmatter');
const metadata = yaml.parse(frontmatter[1]);
if (metadata.version !== process.env.SKILL_VERSION) {
  throw new Error('The skill frontmatter version differs from the Kit version');
}
// Catalog display names can differ from slugs. Match the discovery name
// to the directory the CLI installed and agent-skill@1 will register.
if (metadata.name !== process.env.SKILL) {
  metadata.name = process.env.SKILL;
  fs.writeFileSync(path, `---\n${yaml.stringify(metadata)}---\n${content.slice(frontmatter[0].length)}`);
}
JS
# Use the CLI's discovery path only for staging. The runtime owns the
# composed agent's destinations, so none of this home directory ships.
mkdir -p /out/usr/share/skills-hub
cp -a "$HOME/.claude/skills/$SKILL" "/out/usr/share/skills-hub/$SKILL"
test -f "/out/usr/share/skills-hub/$SKILL/SKILL.md"
test ! -L "/out/usr/share/skills-hub/$SKILL/SKILL.md"
test -s "/out/usr/share/skills-hub/$SKILL/SKILL.md"
chmod -R a+rX /out/usr/share/skills-hub
chown -R 0:0 /out
SH

FROM scratch
COPY --from=build /out /
