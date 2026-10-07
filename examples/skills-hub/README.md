# Skills Hub skill

This mixin installs one public skill from [skills-hub.ai](https://skills-hub.ai)
at build time with the pinned upstream CLI. The default is `code-review`
version `1.0.0`. Choose a skill by its catalog URL slug, and set `version`
to a release listed on that skill's page:

```sh
task kit:build KIT=skills-hub \
  BUILD_ARGS='--build-arg skill=code-review --build-arg version=1.0.0' \
  -- --output type=oci,dest=/tmp/skills-hub-layout,tar=false
task tck:kit:layout DIR=/tmp/skills-hub-layout TAG=1.0.0
```

Use `task kit:dev KIT=skills-hub` to build against the current frontend
source. Pass the same `BUILD_ARGS` to choose another skill. When building
several skills, give each exported image or layout a distinct name;
`kit:build` otherwise reuses the `skills-hub-kit:<version>` tag.

Both arguments resolve at build time. The frontend exports the expanded
v3 descriptor and standard OCI metadata, including the skill's catalog
URL and version. Its `agent-skill@1` source is the literal directory
`/usr/share/skills-hub/<skill>`, containing the installed `SKILL.md` and
any supporting files the installer produces. The build rejects a version
mismatch and aligns the frontmatter `name` with the catalog slug used as
the discovery directory name. No tool provide or shared license is
claimed for arbitrary catalog content; each skill's upstream terms apply.

Compose the built mixin with an agent Kit declaring `agent-skills@1`,
such as `claude` or `codex`. The runtime exposes the bundled skill at
each declared discovery destination before launching the agent. A
workload without a skill destination cannot satisfy this required
capability. Node, npm, the installer, and its home directory stay in the
build stage.

The builder needs access to `registry.npmjs.org` and `api.skills-hub.ai`.
The mixin requests no sandbox network access or credentials: installing
is a build operation, and any permissions needed when using a skill
come from the consuming workload or other mixins.

This example accepts individual public skills. It rejects Skills Hub
compositions, whose children would each need their own `agent-skill@1`
declaration; build a separate mixin for each child instead. Private
organization skills and floating `latest` versions are outside this
example.
