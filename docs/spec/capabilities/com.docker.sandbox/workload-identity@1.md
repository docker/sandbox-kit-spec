# `com.docker.sandbox/workload-identity@1`

A per-sandbox workload identity minted by the runtime: a signing key the
sandbox never holds, trailers that name the sandbox on the commits it
makes, and a ledger record of what the sandbox pushed. The identity
names the sandbox, not the person running it; `git-identity@1` supplies
the person's attribution, and the two compose.

- **Shape**: singleton; every field optional.
- **Permission surface**: **yes** — presence enrolls the sandbox with
  the runtime's identity ledger and discloses the sandbox identifiers to
  processes inside it.

## Config

```yaml
capabilities:
  - type: com.docker.sandbox/workload-identity@1
    description: Commit as this sandbox and record pushes in the ledger
    config:
      trailers: full       # full | short
      ledger: preferred    # required | preferred
```

Use `optional: true` when the Kit can run without an identity.

| Field | Values | Default | Meaning |
|---|---|---|---|
| `trailers` | `full`, `short` | runtime | How much of the identity each commit carries as trailers: `full` writes every identifier the runtime defines, `short` the sandbox id alone. |
| `ledger` | `required`, `preferred` | runtime | What an unreachable ledger means at create: `required` refuses the sandbox, `preferred` creates it and registers once the ledger answers. |

- A stated value **MUST** be one of the spellings above or a reference. <!-- tck: workload-identity@1/values-enumerated -->
- The entry **MUST NOT** state a field this page does not define. <!-- tck: workload-identity@1/fields-closed -->
  Where the identity is registered, which organization owns it and how
  pushes are intercepted are the runtime's answers, not the Kit's.

## Runtime behavior

A conforming runtime:

- **MUST** mint one identity per sandbox before its workload, lifecycle <!-- tck: workload-identity@1/minted-before-launch -->
  hooks and exec sessions run, and keep the private key outside the
  sandbox. Sandbox processes see identifiers and may ask the runtime to
  attest with the key; they never read it.
- **MUST** expose the identity to the workload user as the <!-- tck: workload-identity@1/identity-exposed -->
  `SBX_SANDBOX_ID` environment variable, and **MUST NOT** override the
  Git author or committer: both stay whatever `git-identity@1` or
  repository configuration selects, so the person's signature still
  verifies.
- **MUST** add the identity trailers selected by `trailers`, and a <!-- tck: workload-identity@1/trailers-applied -->
  `Co-authored-by` trailer naming the sandbox, to commits created by the
  workload user through Git's own hook mechanism, so a commit's history
  names the sandbox that made it. A trailer already present is kept.
- **MUST** attribute every push the sandbox makes to the identity and <!-- tck: workload-identity@1/pushes-recorded -->
  record its outcome, accepted or refused, where the runtime's ledger
  can show it.
- **MUST** refuse a required entry when it cannot mint an identity, and <!-- tck: workload-identity@1/unavailable-refuses-required -->
  under `ledger: required` when the ledger is unreachable at create. An
  optional entry is skipped and recorded, and the sandbox starts without
  an identity.
- **MUST NOT** mint an identity for a sandbox whose effective descriptor <!-- tck: workload-identity@1/absent-without-grant -->
  does not carry this capability. A runtime setting may disable
  identity for every sandbox; it cannot enable it for a Kit that did not
  ask.
- **MUST** retire the identity when the sandbox is destroyed and record <!-- tck: workload-identity@1/retired-on-destroy -->
  the retirement, so a commit attributed to it after that point is
  detectable.

The identity's identifiers are not secrets, and the sandbox id is
written into commit history by design. The key is: a runtime that lets
it enter the sandbox has not implemented this capability.

## Composition

A workload, mixin or enclosing set can request the capability. There is
one identity per sandbox, so contributions fold into one entry: `full`
trailers win over `short`, a `required` ledger over `preferred`, and a
required declaration over optional ones.

## Gate

Adding the capability widens permission surface, even when optional: it
enrolls the sandbox with the ledger. The entry contributes its type and
config digest to the runtime-service portion of the surface, so a change
of `trailers` or `ledger` stops for approval like any other config change.
