# `com.docker.sandbox/agent-context@1`

Written instruction content the agent reads — the AGENTS.md family. A host
with no such concept skips an optional declaration or refuses a required
one.

- **Shape**: singleton — at most one entry per descriptor.
- **Permission surface**: **no** — instruction text the agent reads, on the
  entrypoint's trust plane.

## Config

```yaml
# A workload kit: owns the profile, ships its body as a staged file.
- type: com.docker.sandbox/agent-context@1
  config:
    filename: AGENTS.md
    directory: /home/agent/.codex    # explicit agent startup discovery
    contentFile: ./codex-context.md # authored path; staged + rewritten at publish

# A tool mixin: contributes content, owns no profile.
- type: com.docker.sandbox/agent-context@1
  config:
    contentFile: ./gh-context.md

# A content-free kit: small instructions inline.
- type: com.docker.sandbox/agent-context@1
  config:
    content: |
      Use `motd` to inspect the message of the day.
```

| Field | Type | Rules |
|---|---|---|
| `filename` | string | The context-file profile the agent reads (`CLAUDE.md`, `AGENTS.md`, …). Without `directory`, workload Kits only. An agent mixin may declare it together with an explicit `directory`. |
| `directory` | string | Optional absolute, canonical in-sandbox directory in which the runtime writes `filename`. Requires a single-component `filename` in the same entry. May reference a Kit argument or environment value. Omit it to keep the profile beside the workspace. |
| `contentFile` | string | Path to the context body. Authored as a path relative to the build context; the frontend stages the body into the image under `/usr/share/sandbox/kit/<stem>/` and **rewrites this field to the staged in-image path** in the published descriptor. Mutually exclusive with `content`. |
| `content` | string | The body inline, for content-free Kits with no layers to stage into. Mutually exclusive with `contentFile`. |

## Publish behavior

When `contentFile` is set, the frontend reads the authored file, stages it
into the Kit's image filesystem, and publishes the descriptor with
`contentFile` pointing at the staged path. The published artifact is
self-contained: consumers never resolve authored-relative paths.

## Runtime behavior

A conforming runtime:

- **MUST** treat the effective `filename` as the profile file it <!-- tck: agent-context@1/workload-filename-is-profile -->
  materializes for the agent, seeded with the runtime's own guidance. It
  is the workspace directory's sibling when `directory` is omitted,
  keeping the default profile outside the user's checkout.
- **MUST** materialize the profile in the effective `directory` when <!-- tck: agent-context@1/directory-honored -->
  stated, creating the directory when needed. An agent Kit chooses a
  directory its agent discovers at startup: Codex reads its global
  `AGENTS.md` from `CODEX_HOME` (normally `/home/agent/.codex`), while its
  project discovery stops at the repository root and can miss a
  workspace-sibling profile.
- **MUST** preserve existing content outside runtime-managed sections <!-- tck: agent-context@1/existing-content-preserved -->
  when updating a profile. This permits a project discovery location
  without replacing user-authored instructions.
- **MUST** surface each contributing Kit's context **progressively**: the <!-- tck: agent-context@1/progressive-surfacing -->
  profile carries a per-kit index (a "Kits" section) telling the agent
  which Kit contributed what and where to read it on demand — stacking
  mixins does not bloat the always-loaded profile.
- For **staged** content (`contentFile`, published form): the body already
  sits in the assembled image's filesystem, so the runtime **points** the
  agent at the staged path from the index. It does not copy the body.
- For **inline** content (`content`): the runtime writes a per-kit file
  (under a directory beside the profile) and points the index at it.
- **MUST NOT** treat context content as trusted input to the runtime <!-- tck: agent-context@1/content-untrusted -->
  itself: it is prose for the agent. Runtimes SHOULD neutralize any <!-- tck: agent-context@1/content-neutralized -->
  index-management sentinels appearing in kit-supplied text.

## Composition

A workload can provide a legacy profile with `filename` alone. An agent
workload, mixin, or set can provide an explicit profile with `filename`
and `directory` together; ordinary tool mixins contribute bodies alone.

A runtime **MUST** choose an explicit profile over a legacy workload <!-- tck: agent-context@1/explicit-profile-precedence -->
profile, independently of composition order. Multiple explicit profiles
with identical directory and filename describe one destination.

A runtime **MUST** refuse differing explicit profiles as a composition <!-- tck: agent-context@1/explicit-profile-conflict -->
error. Without an explicit profile, at most one contribution owns the
legacy filename, as before.

Each contributing body is indexed in the effective profile, including the
legacy workload's body. Per-kit attribution survives composition — the
index lists Kits individually, in composition order.

The optional `directory` field preserves existing descriptors and their
workspace-sibling default. Context bodies keep their staged paths; only
the profile containing runtime guidance and the per-kit index moves.
Older strict readers reject descriptors using the new field.

See [harness destinations](../../../agent-context-placement.md) for the
example Kits' discovery paths and loader constraints.
