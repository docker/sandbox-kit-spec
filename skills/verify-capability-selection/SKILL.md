---
name: verify-capability-selection
description: Exhaustively verify bounded capability-group selection against the real Go API. Use after changing SelectCapabilities, its records, target identity, optionality, ordering, or cancellation behavior.
---

# Verify capability selection

A group contributes capabilities only when every member is accepted.
A partial group could expose resources after the runtime rejected the
feature that needs them. Cancellation discards every partial result.

Run from the repository root:

```sh
task verify:selection
```

The task needs Python 3 and the Go toolchain named in `go.mod`, currently
Go 1.26.10. The runner selects that exact version with `GOTOOLCHAIN` and
disables workspace overrides with `GOWORK=off`. Go can download the
toolchain and module dependencies on the first run.

## Read the result

The checker enumerates a finite input domain through the actual
`spec.SelectCapabilities` function. No Go theorem prover is configured
in this repository. This is bounded exhaustive verification. A pass
covers the domain below and makes no claim about larger inputs.

The domain contains zero through three top-level items. Each item is an
ordinary entry or a group of one through three members. For every
shape, the checker explores both optionalities, every combination of
accepted and rejected members, and cancellation before entry or inside
each callback, including the last callback. All names are identical and
publisher source paths deliberately differ from consumed paths.

Each baseline checks 33,825 completion cases and 259,689 cancellation
cases. It checks these properties:

| Property | Observable result |
|---|---|
| `atomic-selection` | Only wholly accepted items contribute capabilities. |
| `required-rejection` | Required rejections return an error with each rejected member's consumed path and decision message. Optional rejection alone returns no error. |
| `declaration-order` | Flattening preserves item and member order. |
| `target-identity` | Every member receives one callback with the operation context, owning descriptor, consumption reference, and original member path. Publisher attribution does not replace that path. |
| `decision-records` | Selected and skipped records retain every decision, message, source, and member path. |
| `cancellation` | Cancellation returns `context.Canceled`, no selection, and no later callbacks. |

The runner checks the baseline, compiles six deliberately broken
versions of `spec/groups.go` through Go overlays, then checks the
baseline again. Each broken version must produce the corresponding
`COUNTEREXAMPLE` marker. A compilation failure does not count as a
caught break. Overlays live in a temporary directory. The runner never
edits product code.

A counterexample prints the declarations, acceptance decisions,
cancellation boundary, and observed violation. Read it as a sequence:
the caller presents the listed items, the callback answers each member,
and selection returns the reported capabilities, records, or error.
`CAUGHT` marks an expected failure in a broken version. A failure before
those controls, or in the final baseline, is a finding in current code.

## Check coverage before trusting a pass

Last source review: `c49f7d72625c7b17443ed1d832a29e5fe6eef3a5`.

| Source | Covered responsibility |
|---|---|
| `spec/groups.go`: `SelectCapabilities` | Member callbacks, atomic flattening, required rejection, records, and cancellation. |
| `spec/groups.go`: `SelectionTarget`, `CapabilityDecision`, `SelectionRecord`, `Selection` | The observable API compared by the checker. |
| `docs/spec/SPEC-v3.md` section 7.1.1 | `atomic-selection`, `order`, and the prose contracts for callback identity, decisions, and cancellation. |

Review changes since the recorded commit:

```sh
git diff c49f7d72625c7b17443ed1d832a29e5fe6eef3a5 -- \
  spec/groups.go spec/types.go spec/validate.go spec/capabilities.go \
  docs/spec/SPEC-v3.md
```

Read the changed branches alongside [the checker](scripts/check.go). The expected
selection comes from generated decisions, never from the returned
records. Preserve that independence when extending the checker.
[The runner](scripts/run.py) names the source edits used as negative controls.
If those edits no longer match, review their meaning before updating
them. Do not change the expected property merely to make a failure pass.

When intended API behavior changes, update the properties and domain
after reviewing the normative contract. Run the task and confirm that
every property still catches a real-code break. Move the source-review
commit forward only after confirming that the checker describes the
code at that commit. Use `/maintain-formal-verification-skill` after
changes that did not recheck this coverage.

## Respect the boundary

The generated descriptors are valid workload descriptors with unique,
opaque capability types and fixed attribution metadata. Callbacks
return decisions and may cancel their context. They do not mutate
shared descriptor storage or perform resource application.

The check excludes malformed declarations, type-specific configuration,
argument and environment expansion, merging, fetch snapshots, runtime
resource application, larger descriptors, and cancellation races
between arbitrary Go instructions. Existing unit tests and the TCK
cover different parts of those contracts. This task does not establish
runtime conformance or prove the absence of all selection bugs.
