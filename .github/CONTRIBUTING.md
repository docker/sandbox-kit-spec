# Contributing

Thanks for your interest in the Docker Sandbox Kit Specification.

## Developer Certificate of Origin

Contributions are accepted under the [Developer Certificate of
Origin](https://developercertificate.org/). You certify that you wrote the
patch or have the right to contribute it, by signing off each commit:

```sh
git commit -s
```

which appends a `Signed-off-by` trailer matching your `user.name` and
`user.email`.

## Building and testing

The repository uses [Task](https://taskfile.dev). `task --list` shows
everything; the three you need for a change are:

```sh
task validate   # gofmt + go vet in both modules, library dependency guard
task lint       # golangci-lint + markdownlint, the same configs CI uses
task test       # both modules and the containerd 2.3 consumer check
```

CI runs these on every pull request, so a clean local run is the same
verdict rather than a different one.

The root module contains the public library and `cmd/kit-tck`.
`cmd/frontend` is a separate module so consumers of `assemble`, `fetch`,
and the other library packages do not inherit BuildKit's containerd
requirements. Package paths and public APIs stay the same. Task runs
checks in both modules with `GOWORK=off`; `go test ./...` from the root
only tests the library module.

The frontend replaces its library requirement with `../..`, so builds
and releases use the library in the same checkout. No workspace file is
needed. For editor support across both modules, optionally run:

```sh
go work init . ./cmd/frontend
```

`go.work` and `go.work.sum` stay local and are excluded from Docker
contexts. A workspace combines both dependency graphs, so inspect the
library on its own with `GOWORK=off go list -m all`. Run `go mod tidy`
in each module separately with `GOWORK=off`; do not use `go work sync`
to copy frontend requirements into the library.

`task test:consumer` builds the containerd 2.3 client alongside the
public assemble/fetch APIs in a temporary consumer module, checks that
containerd and its API keep their selected versions, and exercises
assembly and metadata publication without a containerd daemon.

To exercise a change to the BuildKit frontend, build it under a throwaway
tag and point an example at it — BuildKit caches frontend resolution per
reference, so rebuilding a reused tag like `:3` can keep dispatching the
stale binary:

```sh
task kit:dev KIT=hello
```

## Changing the grammar

The descriptor grammar has four representations that must move together,
and tests enforce it:

- `spec/` — the Go types, strict decoding, and validation. This is the
  source of truth; on any disagreement, the code wins.
- `schema/kit.schema.json` and `schema/capabilities/` — the JSON Schema
  used for editor validation. `spec/schema_test.go` pins its constants
  against the spec package so the two cannot drift silently.
- `docs/spec/SPEC-v3.md` and `docs/spec/capabilities/` — the normative
  specification. A capability page states the behavior a conforming
  runtime implements, so a behavior change belongs there too.
- `examples/` — every descriptor under `examples/` must decode and
  validate against the current grammar.

The agent skills under `skills/` teach the grammar too, and nothing tests
them, so a grammar change updates them by hand in the same pull request.

Bear in mind that decoding is strict: any unrecognized field is an error.
Renaming or removing a field therefore breaks kits already published under
the old spelling, which have to be rebuilt rather than degrading. Say so
explicitly in the pull request when a change has that shape.

## Pull requests

Use [Conventional Commits](https://www.conventionalcommits.org/) for commit
subjects (`fix(spec): …`, `feat(frontend): …`). Keep a change and its tests
in the same commit, and separate unrelated fixes into separate commits so
they can be reviewed and reverted independently.
