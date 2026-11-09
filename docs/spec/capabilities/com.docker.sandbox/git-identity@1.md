# `com.docker.sandbox/git-identity@1`

The user's runtime-provided Git author identity, supplied as `user.name`
and `user.email` defaults inside the sandbox. This requests attribution,
not authentication, signing authority or evidence of user review.
Authentication uses separate grants such as `credential@1`.

- **Shape**: singleton, **config-less**.
- **Permission surface**: **yes** — presence discloses the selected name
  and email to the sandbox.

## Execution plan

The default `order` is 200. The scheduled actions are
`setup:provide`.
See [execution order](../../SPEC-v3.md#712-execution-order) for stage
barriers, prerequisite validation, and effective-order reconciliation.

## Config

```yaml
capabilities:
  - type: com.docker.sandbox/git-identity@1
    description: Attribute commits to the user's Git identity
```

Use `optional: true` when the Kit can run without a runtime-provided
identity.

- The entry **MUST NOT** carry `config`, including empty or null. <!-- tck: git-identity@1/no-config -->

The Kit requests an identity, not a configuration source. The runtime
selects and provides the name/email pair according to the user's
settings and its own policy. How it obtains or stores that pair is an
implementation detail; this capability prescribes no files, services,
discovery mechanism or storage location.

## Runtime behavior

A conforming runtime:

- **MUST** provide a complete, nonempty name/email pair selected by the <!-- tck: git-identity@1/runtime-provided -->
  runtime, not supplied by the Kit. An unavailable identity cannot be
  substituted with an identity inferred by sandbox processes.
- **MUST** expose the pair as the workload user's global Git defaults <!-- tck: git-identity@1/global-defaults -->
  before its workload and exec sessions run. `git config --global --get
  user.name` and `user.email` report the selected values. Existing global
  values for these two keys are replaced; unrelated guest settings are
  preserved. Other guest users need not receive the identity.
- **MUST** make the same defaults available to lifecycle hooks running <!-- tck: git-identity@1/before-hooks -->
  as the workload user, before those hooks execute. A hook explicitly
  running as another user is outside this guarantee.
- **MUST** preserve normal repository-local identity precedence. <!-- tck: git-identity@1/local-precedence -->
  The grant does not force `GIT_AUTHOR_*` or `GIT_COMMITTER_*` overrides
  and does not overwrite repository-local configuration.
- **MUST NOT** import unrelated settings from the configuration <!-- tck: git-identity@1/identity-only -->
  sources used to obtain the identity. Credential helpers, HTTP headers,
  includes, aliases, hooks, filters, signing programs and external paths
  are outside the grant. The
  two values are data: serializing them cannot create additional keys
  or execute commands.
- **MUST NOT** expose the configuration sources used to obtain the <!-- tck: git-identity@1/source-private -->
  identity. Resolving a source does not grant access to that source from
  the sandbox.
- **MUST NOT** modify the identity source or allow sandbox edits to update <!-- tck: git-identity@1/source-unchanged -->
  that source through this capability. Materialize defaults in
  sandbox-owned storage.
- **MUST** retain the runtime-provided pair for that sandbox across <!-- tck: git-identity@1/pinned-selection -->
  stop/start and recreate. Later changes to the identity source affect new
  sandboxes, not an existing one's selected identity. Guest edits remain
  subject to the runtime's ordinary writable-layer lifecycle.
- **MUST NOT** disclose the runtime-provided identity through this feature <!-- tck: git-identity@1/absent-without-grant -->
  when the capability is absent or skipped. This does not hide identity
  already present in kit content, commit history or an independently
  shared repository.
- **MUST** refuse a required entry when the runtime withholds the identity or <!-- tck: git-identity@1/unavailable-refuses-required -->
  either value is unavailable. An optional entry is skipped and recorded,
  and the sandbox starts without the runtime-provided identity.

The pair is personal information visible to sandbox processes and to
recipients of commits they create. Granting it neither grants network
access nor permits private signing keys to enter the sandbox.
A runtime can offer an explicit identity override or an off switch.

## Composition

A workload, mixin or enclosing set can request the capability. Identical
requests collapse; a required declaration wins over optional declarations.
There is one runtime-provided pair per sandbox, not one per requesting
Kit.

## Gate

Adding the capability widens permission surface, even when optional.
It contributes its type to the runtime-service portion of the surface.
The name/email values are runtime bindings, not Kit permission
declarations; they do not enter the Kit descriptor or its
permission-surface digest.
