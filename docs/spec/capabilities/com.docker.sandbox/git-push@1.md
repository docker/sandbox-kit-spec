# `com.docker.sandbox/git-push@1`

Bounds on what the sandbox may push over Git: which remotes, which refs,
whether refs may be deleted, and what every pushed commit must carry.
The capability constrains the sandbox rather than granting it anything;
reaching the remote is the network policy's business and authenticating
to it is `credential@1`'s or `ssh-agent@1`'s.

- **Shape**: singleton; each field optional, at least one stated.
- **Permission surface**: **yes** — the bounds are part of what made the
  grant acceptable, so a change to them gates.

## Config

```yaml
capabilities:
  - type: com.docker.sandbox/git-push@1
    description: Push only to feature branches of Docker repositories
    config:
      remotes: [github.com/docker/*]
      refs:
        include: [refs/heads/sbx/*, refs/heads/feature/*]
        exclude: [refs/heads/main, refs/heads/release/*, refs/tags/*]
      delete: deny
      commits:
        committer: enforce     # enforce | warn
        signed: require        # require | prefer | ignore
        trailer: require       # require | warn
```

| Field | Meaning |
|---|---|
| `remotes` | `host/owner/repo` patterns the sandbox may push to. Omitted: any remote. |
| `refs.include` | Patterns a pushed ref must match. Omitted: any ref not excluded. |
| `refs.exclude` | Patterns a pushed ref must not match. Exclude wins over include. |
| `delete` | `deny` refuses a push that deletes a ref. Omitted: the runtime's default. |
| `commits.committer` | `enforce` refuses a commit whose committer is not the sandbox's workload identity; `warn` forwards it and records the mismatch. |
| `commits.signed` | `require` refuses a commit without a signature made through the sandbox's `ssh-agent@1` grant; `prefer` records the absence; `ignore` does not look. |
| `commits.trailer` | `require` refuses a commit missing the workload identity trailers; `warn` forwards it and records the absence. |

A pattern is a literal with at most one `*`, which matches any sequence
of characters including `/`, as a Git refspec glob does: `refs/heads/*`
covers `refs/heads/feature/x`. Ref patterns are fully qualified so a
pattern cannot be read as a branch by one runtime and a tag by another.
A remote is compared without a `.git` suffix.

- The entry **MUST** state at least one field; a policy stating nothing <!-- tck: git-push@1/states-a-rule -->
  bounds nothing, and an empty list is an error rather than "none".
- Every pattern **MUST** match the grammar above. <!-- tck: git-push@1/pattern-grammar -->
- A list **MUST NOT** repeat a pattern. <!-- tck: git-push@1/patterns-unique -->
- Each rule **MUST** be one of its listed spellings or a reference. <!-- tck: git-push@1/values-enumerated -->

## Runtime behavior

A conforming runtime:

- **MUST** judge every ref update of a push against the effective policy <!-- tck: git-push@1/every-update-judged -->
  before the remote applies it, and refuse the update when its remote is
  outside `remotes`, its ref is excluded or not included, or it deletes
  a ref under `delete: deny`. Other updates in the same push proceed.
- **MUST** judge every commit the push introduces against `commits`, and <!-- tck: git-push@1/every-commit-judged -->
  refuse the ref update that carries a commit an `enforce` or `require`
  rule rejects.
- **MUST** refuse a push it cannot inspect when any `commits` rule would <!-- tck: git-push@1/uninspectable-refused -->
  refuse; a push the runtime cannot read cannot be shown to comply.
  Under rules that only warn, the push is forwarded and the gap recorded.
- **MUST** tell the client which rule refused an update, in the push <!-- tck: git-push@1/refusal-explained -->
  response, so the agent can act on it.
- **MUST** record every refusal and every warning where the runtime's <!-- tck: git-push@1/outcomes-recorded -->
  ledger can show it, naming the ref and the rule.
- **MUST** refuse a required entry when it cannot intercept the <!-- tck: git-push@1/unavailable-refuses-required -->
  sandbox's pushes, including when the sandbox has no workload identity
  to attribute them to. An optional entry is skipped and recorded.
- **MUST NOT** let a Kit relax a bound the runtime enforces for every <!-- tck: git-push@1/host-floor -->
  sandbox: where the runtime and the Kit both state a rule, the stricter
  one applies.

Interception is at the runtime's egress: a sandbox that reaches a remote
the runtime does not intercept is outside this capability, and a runtime
that cannot close that path refuses the entry rather than enforcing
part of it.

## Composition

A workload, mixin or enclosing set can declare the capability.
Contributions intersect, so a mixin can only narrow what the workload
allows: `exclude` lists union; `deny` wins over `allow`; the stricter
commit rule wins. Two different `remotes` or `refs.include` lists have no
single-list intersection and are refused; one Kit adopts the other's
list. A required declaration wins over optional ones.

## Gate

The entry contributes its type and config digest to the runtime-service
portion of the surface. Any change to the bounds, narrowing included,
stops for approval: a user who accepted one policy has not accepted
another.
