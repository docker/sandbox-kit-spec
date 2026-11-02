# Skills

Agent skills for working with this specification: authoring a v3 kit,
porting one from the v2 `spec.yaml` grammar, and composing published kits
into an `sbxenv.yaml` environment.

| Skill | Use it for |
|---|---|
| [`create-kit-v3`](create-kit-v3/SKILL.md) | Writing a new kit — choosing workload or mixin, the descriptor, the content recipe, capabilities, version pinning, and verification. |
| [`migrate-kit-to-v3`](migrate-kit-to-v3/SKILL.md) | Porting a v2 kit — the field-by-field mapping and the judgment calls a migration turns on. |
| [`sbxenv`](sbxenv/SKILL.md) | Creating or updating an `sbxenv.yaml` — suggesting v3 kits from the live Docker Hub catalog, kit and environment args, credential bindings and secrets, and refreshing kit tags and digests. |

They are ordinary markdown and useful to read directly. Nothing in them is
specific to one agent tool: each is a directory holding a `SKILL.md` whose
frontmatter carries only `name` and `description`, which is the convention
Cursor and Claude Code both read, and reference files beside it that the
`SKILL.md` links to. `sbxenv` also carries a standard-library Python
helper under `scripts/` that queries Docker Hub when it runs, because the
kit catalog, its tags and its digests change too often to write down.

## Wiring them into a tool

This directory is the source of truth, and the repository carries no
tool-specific copy of it. Where an agent expects to find skills is a local
choice, so make the link locally — both paths below are git-ignored:

```sh
for d in .cursor .claude; do
  if [ -e "$d/skills" ] && [ ! -L "$d/skills" ]; then
    echo "$d/skills exists and is not a symlink; move it aside first" >&2
  else
    mkdir -p "$d" && ln -sfn ../skills "$d/skills"
  fi
done
```

The guard earns its lines. `mkdir -p` is needed because the repository
carries neither directory, so both are absent from a fresh clone. `-sfn` replaces an existing
*symlink* rather than following it. But neither handles `skills` already being
a **real directory**: `ln` then treats it as the destination and silently
creates `.cursor/skills/skills` pointing at its own parent. That is why the
real-directory case is refused rather than linked — you have skills of your
own there, and this should not bury them under a loop.

A symlink rather than a copy, so there is only ever one version to keep
correct. An agent with no skills mechanism at all loses nothing: pass the
relevant `SKILL.md` as context, or read it and follow it.

## Keeping them honest

Both skills document commands and failure modes that were verified against
real artifacts rather than inferred from the specification, and they say so
where it matters. When a claim in one stops being true — a tool grows a flag,
a private repository becomes public, the frontend starts catching something it
used to let through — the fix belongs in the skill, because its value is
precisely that its checks are known to catch the bugs it names.
