# `com.docker.sandbox/cross-app-access@1`

Lets the sandbox reach one resource application as the person who runs
it, with that person's enterprise identity provider deciding whether it
may. The runtime holds the person's sign-in to the identity provider.
For each grant it asks the provider for an identity assertion grant
(the IETF Identity Assertion JWT Authorization Grant, which identity
providers ship as Cross App Access) naming the application's
authorization server, redeems the grant there for an access token, and
injects that token into matching outbound requests at the network
boundary. Neither the sign-in, the grant nor the access token enters
the sandbox, and no consent screen interrupts the agent: the identity
provider's administrator has already decided which applications an
agent may reach for which people, and can withdraw that decision in one
place.

Where `workload-token@1` presents the sandbox as itself, this capability
presents the person, through the sandbox, to an application that
already knows them.

- **Shape**: instance-shaped on `name`; `resource` and `scopes` are
  optional.
- **Permission surface**: **yes**. Presence lets the sandbox act as the
  person toward the application, and records each exchange in the
  runtime's ledger.
- **Requires**: `workload-identity@1` in the effective descriptor. The
  sandbox identity signs the ledger record of every exchange, so access
  taken in a person's name is attributable to one sandbox.

## Config

```yaml
capabilities:
  - type: com.docker.sandbox/workload-identity@1
  - type: com.docker.sandbox/cross-app-access@1
    description: Read the person's todos
    config:
      name: todos
      issuer: https://auth.example.com
      resource: https://api.example.com
      scopes: [todos.read]
      inject:
        - domain: api.example.com
          header: Authorization    # default
          format: "Bearer %s"      # default
```

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | Identifies the grant among the sandbox's tokens and names it in the ledger. `[A-Za-z0-9][A-Za-z0-9_.-]{0,63}`. |
| `issuer` | yes | Issuer identifier of the application's authorization server (RFC 8414): the audience of the identity assertion grant, and the server that redeems it. An `https` URL of at most 256 characters without userinfo, query or fragment. |
| `resource` | no | Resource indicator the access token is bound to (RFC 8707): an absolute URI of at most 256 characters without a fragment. |
| `scopes[]` | no | Scopes the access token may carry, each one scope token (RFC 6749 section 3.3) of at most 128 characters, listed once. Absent leaves the choice to the identity provider's policy. |
| `inject[]` | yes, at least one | Outbound domains the runtime presents the access token to. `header` defaults to `Authorization`, `format` to `Bearer %s` and renders the token with exactly one `%s`. `scheme` and `username` do not apply. |

- A stated `name` **MUST** match the grammar above or be a reference. <!-- tck: cross-app-access@1/name-grammar -->
- A stated `issuer` **MUST** be an `https` URL within the bounds above, <!-- tck: cross-app-access@1/issuer-grammar -->
  or a reference.
- A stated `resource` **MUST** be an absolute URI without a fragment, or <!-- tck: cross-app-access@1/resource-grammar -->
  a reference.
- Each of `scopes[]` **MUST** be one scope token or a reference, and a <!-- tck: cross-app-access@1/scopes-grammar -->
  literal scope is listed once.
- Every `inject[].domain` **MUST** appear in the network policy's runtime <!-- tck: cross-app-access@1/inject-within-allow -->
  allow list, matched exactly, or be covered by a bare wildcard: a token
  is presented only where the sandbox may already reach.
- An `inject[].domain` **MUST NOT** also be listed by another <!-- tck: cross-app-access@1/domain-exclusive -->
  `cross-app-access@1` entry or by a `workload-token@1` entry. A domain
  takes one runtime-held token, so a request never has to say whose
  authority it wants.
- The entry **MUST NOT** state a field this page does not define. Which <!-- tck: cross-app-access@1/fields-closed -->
  identity provider the person signed in to, which client the runtime is
  at the application, and how long a token lives are the runtime's and
  the administrators' answers, not the Kit's.

## Runtime behavior

A conforming runtime:

- **MUST** obtain each identity assertion grant from the identity <!-- tck: cross-app-access@1/person-authorized -->
  provider the person signed in to, in exchange for that sign-in,
  outside the sandbox. A grant the provider refuses is not replaced by
  any other credential the runtime holds for the application.
- **MUST** request the grant for exactly the declared `issuer` as its <!-- tck: cross-app-access@1/grant-bound -->
  audience and the declared `resource`, and for no scope outside the
  declared `scopes`.
- **MUST** redeem the grant only at the token endpoint published in the <!-- tck: cross-app-access@1/redeemed-at-issuer -->
  declared issuer's authorization server metadata, after checking that
  the metadata names that issuer.
- **MUST** present the access token only on HTTPS requests from the <!-- tck: cross-app-access@1/injected-at-boundary -->
  sandbox to a listed `inject[].domain`, rendered into the listed
  header.
- **MUST NOT** make the person's sign-in, the grant, the access token or <!-- tck: cross-app-access@1/never-in-sandbox -->
  the runtime's client credential readable inside the sandbox: not as an
  environment variable, a file, or a response the runtime itself
  produces. What a listed application returns is the Kit author's
  choice.
- **MUST** replace an access token before it expires, by a new exchange <!-- tck: cross-app-access@1/fresh-token -->
  with the identity provider, so that a withdrawn permission takes
  effect no later than the current token's expiry.
- **MUST NOT** present an expired token; a request that arrives while no <!-- tck: cross-app-access@1/expired-refused -->
  valid token can be obtained is refused rather than sent without one.
- **MUST** record each exchange in the runtime's ledger with the grant's <!-- tck: cross-app-access@1/exchange-recorded -->
  name, the identity provider, the `issuer`, the `resource`, the scopes
  granted, the person's subject at the identity provider and the access
  token's expiry.
- **MUST NOT** record the sign-in, the grant or the access token, in the <!-- tck: cross-app-access@1/token-not-recorded -->
  ledger or anywhere a later reader could replay it.
- **MUST** refuse a required entry when the effective descriptor carries <!-- tck: cross-app-access@1/unavailable-refuses-required -->
  no `workload-identity@1`, when the person has not signed in to an
  identity provider, or when the runtime is not a registered client of
  the declared issuer. An optional entry is skipped and recorded, and
  requests to its domains leave without a token.
- **MUST NOT** present a token for a sandbox whose effective descriptor <!-- tck: cross-app-access@1/absent-without-grant -->
  does not carry this capability for that issuer.

The access token is the person's, narrowed twice: by the identity
provider's policy for the runtime's client, and by the Kit's `resource`
and `scopes`. The identity provider records which client asked, the
application records which person it served, and the runtime's ledger
ties both to one sandbox.

## Composition

A workload, mixin or enclosing set can request a grant. Entries
reconcile on `name`: identical restatements collapse and a required
declaration wins over optional ones; two entries with one `name` but a
different `issuer`, `resource`, `scopes` or `inject` list conflict.
Different names coexist, so a workload can carry one grant per
application, each on its own domains.

## Gate

Adding the capability widens permission surface, even when optional: it
lets the sandbox act for the person at the application. The entry
contributes its type and config digest to the runtime-service portion
of the surface, so a change of `issuer`, `resource`, `scopes` or
`inject` stops for approval like any other config change.
