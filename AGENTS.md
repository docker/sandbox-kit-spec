# Working in this repository

Guidance for AI coding agents (and the humans steering them) making
changes to the Docker Sandbox Kit Specification. It is the operational
companion to [CONTRIBUTING.md](.github/CONTRIBUTING.md) and
[GOVERNANCE.md](GOVERNANCE.md): those say what a change must carry, this
says how to produce and verify one without surprising a reviewer.

## What this repository is

Three things that move together:

| Part | Where | What it is |
|---|---|---|
| The specification | `docs/spec/SPEC-v3.md`, `docs/spec/capabilities/`, `docs/spec/conformance.md` | Normative text. In SPEC-v3 and the capability pages every MUST/SHOULD is anchored and accounted for by a check or a waiver; `conformance.md` binds adapters and the suite itself and carries no anchors |
| The reference implementation | `spec/`, `resolve/`, `assemble/`, `fetch/`, `cmd/frontend/`, `schema/` | Go types and validation (the grammar's source of truth), the resolver, the BuildKit frontend, and the JSON Schema pinned to the Go types |
| The conformance suites | `tck/`, `cmd/kit-tck/` | `kit-tck validate` judges a published Kit; `kit-tck runtime` judges a runtime through an adapter. `tck/sandbox` also judges the suite itself against a fake adapter |

Plus `examples/` (every descriptor must decode and validate against the
current grammar; tests enforce it), `skills/` (tool-agnostic agent skills
for authoring and migrating Kits; nothing tests them), and
`scripts/kit-version.sh` (the map of which example Kits track an upstream
release).

When two representations disagree — Go types, JSON Schema, normative
prose, an example — the code in `spec/` wins, and the fix is to bring the
others back in line, not to special-case the reader.

## Before you start

- Read the relevant normative page before touching behavior. A change to
  what a runtime must do is a change to a capability page first, and to
  the code second.
- When authoring or porting a Kit under `examples/`, read and follow
  `skills/create-kit-v3/SKILL.md` or `skills/migrate-kit-to-v3/SKILL.md`.
  They are the repository's own instructions for that job, and they were
  verified against real artifacts.
- `task --list` names every task with its purpose. Prefer a Task target
  over the underlying `go`/`docker` invocation: the targets carry guards
  and pinned versions that a hand-typed command does not.

Toolchain the tasks assume: Go at the version in `go.mod`,
[Task](https://taskfile.dev), `golangci-lint` v2, Docker with buildx (for
`lint:md`, `kit:dev`, `test:e2e`), and the `sbx` CLI only for
`tck:runtime:sbx`.

## Verifying a change

Run what CI runs, from the fast signal outward. CI runs `validate`,
`lint`, `test:unit`, and `test:tck` on every pull request, so a clean
local run is the same verdict rather than a different one.

| Task | What it does | When |
|---|---|---|
| `task validate` | `gofmt -l` + `go vet` | Every Go change. Seconds |
| `task lint` | `golangci-lint` and `markdownlint` with the repo configs (`lint:go`, `lint:md`) | Every change. `lint:md` needs Docker; it runs a digest-pinned image over every `**/*.md` |
| `task test:unit` | Every Go test except the sandbox conformance suite. Includes decoding and validating every descriptor under `examples/` and the schema-to-spec pins | Every change. Under a minute |
| `task test:tck` | The sandbox conformance suite against the fake adapter, including every fake-adapter mutation | Any change under `tck/`, `docs/spec/`, or to a capability page. Minutes |
| `task test` | `test:unit` and `test:tck` together, as one `go test ./...`. It does not run `validate` or `lint`; those are separate | Before opening a pull request, alongside `validate` and `lint` |
| `task kit:dev KIT=hello` | Builds the frontend from the current source under a unique tag and builds one example Kit against it | Any change under `cmd/frontend/`. Needs Docker with buildx |
| `task kits:dev` | The same for every Kit in `KITS` | A frontend change that touches what every descriptor goes through (decoding, staging, export) |
| `task test:e2e` | Publishes two Kits to a throwaway registry and builds a set over them | A change to set building, `fetch/`, or `assemble/`. Needs Docker; CI does not run it |
| `task tck:kit REF=<ref>` | `kit-tck validate` against a published Kit | When you changed what the artifact rules check and have a real Kit to judge |
| `task tck:runtime:sbx` | The full runtime suite against Docker Sandboxes | Do not run unprompted: it needs a signed-in `sbx`, drives hundreds of sandbox lifecycles, and takes up to two hours |

Narrowing is fine while iterating (`go test ./spec/...`,
`go test ./tck/sandbox/ -run TestEveryStatement`), but the tasks above
are what a reviewer expects to have passed.

Do not iterate on frontend changes by rebuilding `docker/sandbox-kit:3`
locally: BuildKit caches frontend resolution per reference and can keep
dispatching the stale binary. `kit:dev` and `frontend:dev` exist for
exactly this reason.

## Branches

Use semantic branch names in the form `<type>/<short-description>`,
with a type from the commit conventions below and a lowercase,
hyphen-separated description of the change. Examples:
`fix/resolver-cancellation`, `feat/kit-inspection`, and
`docs/branch-naming`.

Do not include AI tool, model, agent, or harness identifiers in branch
names: prefixes such as `codex/`, `claude/`, or `cursor/` expose local
tooling in repository history and pull requests. This convention
overrides tool-generated branch naming defaults. If a tool creates a
branch with such a name, rename it before pushing or opening a pull
request.

## Commits

Commit subjects follow
[Conventional Commits](https://www.conventionalcommits.org/):

```text
<type>(<scope>): <imperative, lowercase summary, no trailing period>
```

Types in use, most to least common: `fix`, `feat`, `docs`, `chore`, `ci`,
`test`, `refactor`, `perf`. Scopes are the area the change lands in:
`spec`, `schema`, `frontend`, `tck`, `kit-tck`, `resolve`, `assemble`,
`fetch`, `examples`, `skills`, `readme`, `task`, `deps`, `version`,
`builder`. A change spanning two areas lists both (`fix(spec,tck): …`);
a change with no single home omits the scope (`feat: expand example
runtime kits`). A breaking change marks the type with `!`
(`feat(kit-tck)!: add inspect, rename kit to validate`) and says what
breaks in the body.

Examples from the history:

```text
fix(kit-tck): honor cancellation in the layer walk
feat(spec): derive a workload's provides from the packages its image carries
docs(skills): add v3 kit authoring and migration skills
chore(deps): bump the go-modules group with 3 updates
```

The body explains why, in prose, when the diff cannot. This codebase
comments its reasoning heavily — a guard "earns its lines", a pinned
digest says what a moved tag would have broken — and commit messages
follow the same habit. Do not restate the diff.

Rules that hold for every commit:

- One logical change per commit, with its tests. Unrelated fixes go in
  separate commits so they can be reviewed and reverted independently.
- Sign off every commit (`git commit -s`). Contributions are accepted
  under the Developer Certificate of Origin, and the `Signed-off-by`
  trailer must match the author's `user.name` and `user.email`. Never
  fabricate a sign-off for someone else; if you are committing on a
  user's behalf, the identity is theirs and the sign-off is their
  decision.
- Never amend, rebase, or force-push over commits you did not create in
  this session unless explicitly asked.
- Do not hard-wrap the body at 72 or 80 columns. Write one paragraph per
  line and let the viewer wrap it: GitHub preserves line breaks in
  commit messages, so manual wrapping fixes where each line ends. The
  72-column habit belongs to Markdown files in the tree (see Style),
  not to messages.

## Pull requests

The title is a Conventional Commit subject, exactly as a commit's would
be — pull requests are merged with a merge commit whose body is the
title, so a title that breaks the convention breaks the history.

The body follows [the template](.github/PULL_REQUEST_TEMPLATE.md), which
has two parts:

1. **What changed and why.** If the change alters the grammar, a
   capability contract, or a conformance duty, name the normative
   statements that moved and how each is now accounted for: the anchor
   and the check that judges it, or the anchor and the recorded waiver.
   If the change renames or removes a descriptor field, say so
   explicitly — strict decoding means Kits already published under the
   old spelling stop decoding rather than degrading, and a reviewer
   needs that stated rather than inferred.
2. **A `release-note` block.** One user-facing sentence, or `NONE`. Keep
   the fenced block and its `release-note` info string intact; that is
   the shape a release reads from.

Keep the pull request as small as its subject. A refactor that a fix
needed is a separate commit in the same pull request, not a second
subject buried in the first.

As with commit bodies, do not hard-wrap the description: one paragraph
per line. Pull request descriptions render as Markdown; this is a
source-editing convention so prose can be revised without reflowing
lines to a fixed column width. Keep intentional Markdown structure,
such as lists and code blocks.

## Changing the grammar

The descriptor grammar has four representations, and tests hold them
together. A change to one is a change to all four in the same pull
request:

1. `spec/` — the Go types, strict decoding, validation. Source of truth.
2. `schema/kit.schema.json` and `schema/capabilities/` — the JSON Schema.
   `spec/schema_test.go` pins its constants (need types, enums, field
   regexes) against the spec package, so a drift fails `test:unit`.
3. `docs/spec/SPEC-v3.md` and `docs/spec/capabilities/` — the normative
   text, with anchors (below).
4. `examples/` — every descriptor must still decode and validate.

And a fifth that nothing tests: the skills under `skills/` teach the
grammar, so update them by hand.

Decoding is strict: an unrecognized field is an error, not a warning.
That means **adding a field is not free** — a descriptor using it cannot
be read by a frontend or runtime built before it. Before changing a
capability's config, read the versioning rules in
[RELEASES.md](RELEASES.md): a config schema that has shipped in a tagged
release moves its `@N` version and the old version stays published,
unless maintainers approve the draft-stage exception recorded there; a
capability that has never shipped may still change in place; a
top-level descriptor change is a `schemaVersion` move and is treated as a
new specification document. Do not bump `schemaVersion` or a capability
version without stating in the pull request which rule forced it.

## Changing normative text

`docs/spec/SPEC-v3.md` and the capability pages are held to an
accounting that `tck/sandbox/coverage_test.go` enforces, and `test:tck`
fails when it slips:

- Every statement using **MUST**, **MUST NOT**, **SHOULD**, or **SHOULD
  NOT** carries a `<!-- tck: <id> -->` anchor on the line that opens it
  (a bullet, a table row, or a paragraph's first line). The anchor is
  invisible in rendered docs and is the statement's stable identity; do
  not rename one without updating every check, cover, and waiver that
  names it.
- Every anchored statement is judged by a check whose requirement id
  matches it, listed under another check's entry in the `covers` or
  `kitCovers` maps, or recorded in `waived` with a reason. A statement
  that is both waived and covered fails: drop the waiver.
- An **observable** runtime duty comes with a fixture and a fake-adapter
  mutation in `tck/sandbox/sandbox_test.go`, so the suite proves the
  check fails when the behavior is absent. A check no mutation can fail
  is a check nobody has seen fail, and the suite refuses it.
- A duty the suite cannot observe records a waiver saying what cannot be
  observed yet. A waiver is a decision reviewed like any other line, not
  a way past the guard.

`docs/spec/conformance.md` is outside this accounting by design: it
binds adapters and the suite itself.

Use RFC 2119 keywords only where you mean them. A sentence that reads
like a requirement but is not one is either mislabeled or missing an
anchor, and the guard will tell you which.

## Example Kits

- The first line of a descriptor is `# syntax=docker/sandbox-kit:3` and
  must stay first; the `yaml-language-server` modeline, when present, is
  line two.
- Kits that track an upstream release (`scripts/kit-version.sh kits`
  lists them) have their version default refreshed by
  `task versions:update KIT=<kit>`, not by hand — the tag a build
  publishes under is read from the same map, so a hand edit and the
  script can disagree about latest. `task versions:check` reports without
  editing.
- A new example Kit joins `KITS` in `Taskfile.yaml` unless it is a set
  whose members are registry references (`team`, `claude-acp-set`,
  `codex-acp-set`), which only build once those references exist. It is
  tested automatically by `spec/examples_test.go` or, for a
  comment-descriptor `.dockerfile`, `cmd/frontend/examples_test.go`.
- A `-mixin` variant of a workload Kit is its own directory with its own
  descriptor; the skills say when one is warranted.

## Things not to do

- Do not push images or tags. `kit:push` refuses without `REGISTRY`, and
  `frontend:push` refuses without `FRONTEND_PUSH_OK=1`, because
  `docker/sandbox-kit:<tag>` is a public syntax reference that every
  descriptor resolves. Those guards are not yours to satisfy. Releases
  are `v3.X.Y` git tags pushed by a maintainer; see RELEASES.md.
- Do not edit `go.sum` by hand, and do not bump the commit SHAs pinned
  on GitHub Actions: Dependabot owns the Go module and the action pins,
  and moves the version comment beside each SHA with it. The tool
  versions the workflows and Taskfile pin (`golangci-lint`, `task`, the
  `markdownlint-cli2` image digest) are deliberate and shared between CI
  and local runs; changing one is its own `ci:` or `chore:` commit with
  the reason, never a side effect of another change.
- Do not relax a lint rule to make a change pass. `.golangci.yml` and
  `.markdownlint.yml` each say why every disabled rule is disabled; a new
  exemption needs the same justification and belongs in its own commit.
- Do not add tool-specific copies of the skills. `skills/` is the source
  of truth; `.cursor/skills` and `.claude/skills` are git-ignored local
  symlinks (see `skills/README.md`). `.cursor/`, `.claude/`, and
  `.vscode/` local settings stay uncommitted.
- Do not commit build outputs (`/frontend`, `/kit-tck`, `/bin`, `/dist`)
  or anything under `/tmp` a task created. `.gitignore` already covers
  them; do not widen it to hide a new artifact.
- Do not add a `.md` file under `tck/**/testdata/`; conformance fixtures
  are test inputs the suite asserts on, not documentation, and the
  markdownlint ignore list is scoped to exactly that.

## Style

- Go: `gofmt`, `go vet`, and golangci-lint's standard set are the floor.
  Tests use `testify/require`. Errors are returned, not logged and
  swallowed.
- Comments explain a decision the reader could not recover from the
  code: why a guard exists, what breaks without it, which spelling is
  portable between BSD and GNU tools. Do not comment what the next line
  does.
- Markdown is wrapped by hand at roughly 72 columns; tables use compact
  separators (`|---|---|`); no emoji. `MD013` (line length) is off
  because the specification hand-wraps and carries long reference
  tables, not as a license for one-line paragraphs.
- The house voice is plain and declarative, and states the reason next to
  the rule. Match it in docs, comments, and commit bodies alike.
