# pstack skills

An agent-neutral mixin bundling all 58 skills from the standalone
[pstack mirror](https://github.com/backnotprop/pstack): engineering
workflows, playbooks, and principles. No agent binary is included.

Compose it with an agent workload from the repository root:

```sh
sbx run ./examples/codex --kit ./examples/pstack .
sbx run ./examples/claude --kit ./examples/pstack .
```

Invoke `poteto-help` for help choosing a skill, `poteto-mode` for the
main workflow, or an individual skill such as `how`, `tdd`, or `unslop`.
The agent's own invocation syntax applies; installing the collection
does not activate its workflows automatically.

Each skill declares an optional `agent-skill@1` source under
`/usr/share/pstack-skills/`. These small discovery wrappers preserve
the upstream frontmatter and direct the agent to the absolute bundled
`SKILL.md` path. Copying a wrapper into an agent's discovery store keeps
links to sibling skills and guides rooted in the original tree, even
when no context capability is selected. Agent Kits that declare
`agent-skills@1` receive the wrappers at every selected discovery
destination. The singular capability supplies a bundled skill; the
plural capability declares an agent's discovery directory. Optional
context entries offer both
`agent-context@1` and `agent-context@2` during the runtime rollout;
select one. They point other agents to `/usr/share/pstack/skills/`,
where each skill can be read directly. A runtime supporting neither
discovery nor context still carries the files, but the user must point
the agent at them.

The complete upstream tree lives under `/usr/share/pstack/`, including
the MIT license, README, guides, agent templates, playbooks, and scripts.
Read skills from that source tree when following relative links outside
a skill directory. The Kit does not write agent-specific configuration
or install the Cursor plugin. Custom subagent registration, model
selection, external tools, and services depend on the consuming agent;
the upstream skills describe adaptations for other harnesses. Bundled
scripts are available on demand, without installing their dependencies
or running them during registration.

## Build and verify

```sh
task kit:build KIT=pstack \
  -- --builder kit-builder \
  --output type=oci,dest=/tmp/pstack-layout,tar=false
task tck:kit:layout DIR=/tmp/pstack-layout TAG=0.15.36
```

Use a Docker container builder with OCI export support; see the
[authoring skill](../../skills/create-kit-v3/SKILL.md#build-run-verify)
for setup. Use `task kit:dev KIT=pstack` when working on the frontend.

The snapshot is commit
`a6342ded43a2437e3c664b46c2cff103787773fd`, whose plugin manifests both
report `0.15.36`. The mirror has no release tags. The recipe pins the
commit and archive SHA-256, then verifies both manifest versions against
the descriptor's version argument. That argument accepts only this
snapshot's version; it cannot select arbitrary upstream releases.

To update the snapshot, review the new commit, change the archive URL
and checksum in the recipe, and update the version default and enum in
the descriptor. Keep the `agent-skill@1` entries aligned with the
upstream `skills/` directories, then rebuild and validate the artifact.
This example is deliberately outside `versions:update`: a plugin
version does not identify an immutable mirror snapshot.

The builder needs `codeload.github.com` for the source archive, plus
access to its base image and Debian package repositories. All content
is bundled at build time. Runtime network access and credentials come
from the consuming workload or other mixins.
