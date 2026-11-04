# `com.docker.sandbox/workload-token@1`

Presents the sandbox's workload identity to one relying party. The
runtime asks its identity issuer for a short-lived token whose `aud` is
the declared audience and whose subject is the sandbox, then injects it
into matching outbound requests at the network boundary. The token is
the identity's credential for that one party; the runtime never places
it in the sandbox, so the agent cannot read, log or forward it unless a
listed relying party hands it back. A relying party verifies it against
the issuer's published keys, or hands it to a cloud identity provider
(AWS IAM, GCP Workload Identity Federation) that exchanges it for cloud
credentials bound to the same sandbox. A Kit that lists the issuer's own
token endpoint as an inject domain chooses the latter route for clients
that read an identity token from a URL, such as cloud CLIs: the sandbox
then obtains its own short-lived, audience-bound token and nothing else.

- **Shape**: instance-shaped on `name`; every field required.
- **Permission surface**: **yes** — presence lets the sandbox act as
  itself toward the audience, and records each issuance in the runtime's
  ledger.
- **Requires**: `workload-identity@1` in the effective descriptor. The
  token is that identity's; a sandbox without one has nothing to present.

## Config

```yaml
capabilities:
  - type: com.docker.sandbox/workload-identity@1
  - type: com.docker.sandbox/workload-token@1
    description: Call the deployment API as this sandbox
    config:
      name: backend
      audience: https://api.example.com
      inject:
        - domain: api.example.com
          header: Authorization    # default
          format: "Bearer %s"      # default
```

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | Identifies the token among the sandbox's tokens and names it in the ledger. `[A-Za-z0-9][A-Za-z0-9_.-]{0,63}`. |
| `audience` | yes | The token's `aud` claim: the relying party, or the identity provider that exchanges it, which **must** verify it. At most 256 characters, no whitespace. |
| `inject[]` | yes, at least one | Outbound domains the runtime presents the token to. `header` defaults to `Authorization`, `format` to `Bearer %s` and renders the token with exactly one `%s`. `scheme` and `username` do not apply. |

- A stated `name` **MUST** match the grammar above or be a reference. <!-- tck: workload-token@1/name-grammar -->
- A stated `audience` **MUST** be non-empty without whitespace, or a <!-- tck: workload-token@1/audience-grammar -->
  reference.
- Every `inject[].domain` **MUST** appear in the network policy's runtime <!-- tck: workload-token@1/inject-within-allow -->
  allow list, matched exactly, or be covered by a bare wildcard: a token
  is presented only where the sandbox may already reach.
- The entry **MUST NOT** state a field this page does not define. Which <!-- tck: workload-token@1/fields-closed -->
  issuer signs, how long a token lives and what claims it carries are the
  runtime's answers, not the Kit's.

## Runtime behavior

A conforming runtime:

- **MUST** obtain each token from the identity issuer that minted the <!-- tck: workload-token@1/issuer-minted -->
  sandbox's `workload-identity@1`, proving possession of the sandbox key
  outside the sandbox.
- **MUST** set the token's audience to exactly the declared `audience`. <!-- tck: workload-token@1/audience-exact -->
- **MUST** present the token only on HTTPS requests from the sandbox to a <!-- tck: workload-token@1/injected-at-boundary -->
  listed `inject[].domain`, rendered into the listed header.
- **MUST NOT** make the token readable inside the sandbox other than by <!-- tck: workload-token@1/never-in-sandbox -->
  presenting it to a listed domain: not as an environment variable, a
  file, or a response the runtime itself produces. What a listed relying
  party returns is the Kit author's choice.
- **MUST** replace a token before it expires. <!-- tck: workload-token@1/fresh-token -->
- **MUST NOT** present an expired token; a request that arrives while no <!-- tck: workload-token@1/expired-refused -->
  valid token can be obtained is refused rather than sent without one.
- **MUST** record each issuance in the runtime's ledger with the token's <!-- tck: workload-token@1/issuance-recorded -->
  name, audience, identifier and expiry.
- **MUST NOT** record the token itself, in the ledger or anywhere a <!-- tck: workload-token@1/token-not-recorded -->
  later reader could replay it.
- **MUST** refuse a required entry when the effective descriptor carries <!-- tck: workload-token@1/unavailable-refuses-required -->
  no `workload-identity@1`, or when the issuer cannot mint for the
  sandbox at create. An optional entry is skipped and recorded, and
  requests to its domains leave without a token.
- **MUST NOT** present a token for a sandbox whose effective descriptor <!-- tck: workload-token@1/absent-without-grant -->
  does not carry this capability for that audience.

The token identifies the sandbox, not the person; a relying party that
needs the person's authorization keeps asking for it separately. Its
lifetime is short by design, so a captured request replays for minutes,
not days, and the ledger shows which sandbox obtained which token when.

## Composition

A workload, mixin or enclosing set can request a token. Entries reconcile
on `name`: identical restatements collapse and a required declaration
wins over optional ones; two entries with one `name` but a different
`audience` or `inject` list conflict. Different names coexist, so a
workload can carry one token per relying party.

## Gate

Adding the capability widens permission surface, even when optional: it
lets the sandbox authenticate as itself to the audience. The entry
contributes its type and config digest to the runtime-service portion
of the surface, so a change of `audience` or `inject` stops for approval
like any other config change.
