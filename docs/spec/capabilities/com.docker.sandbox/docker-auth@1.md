# `com.docker.sandbox/docker-auth@1`

Registry authentication through opaque, run-scoped placeholders. Real
login credentials and issued tokens remain outside the sandbox.

- **Shape**: whole-entry; identical requests collapse.
- **Permission surface**: the complete config contributes to the
  runtime-service surface. Adding registries requires approval.

## Config

```yaml
capabilities:
  - type: com.docker.sandbox/docker-auth@1
    config:
      registries: [docker.io, ghcr.io, registry.example.com]
```

The config **MUST** contain a nonempty `registries` list of distinct, <!-- tck: docker-auth@1/config-valid -->
exact lowercase DNS names containing a dot, at most 253 characters long,
with labels of at most 63 characters and no leading or trailing hyphen.
Schemes, ports, IP addresses, paths, wildcards, empty labels, unknown
fields and null values are invalid. Each entry identifies a registry the
Kit needs to authenticate to.

## Runtime behavior

A conforming runtime:

- **MUST** use runtime-provided credential bindings only for admitted <!-- tck: docker-auth@1/host-resolution -->
  registries. Credential discovery and storage are runtime choices.
  Missing, withheld or unsupported credentials refuse a required request;
  an optional request is skipped and recorded.
- **MUST** confine each login grant to authentication for its admitted <!-- tck: docker-auth@1/request-scope -->
  registry, including authentication services acting for that registry.
  Sandbox-controlled requests cannot disclose credentials to unrelated
  destinations or authenticate to ungranted registries. Traffic remains
  subject to network policy and applicable request restrictions, with
  credentials protected in transit.
- **MUST NOT** expose real login usernames, passwords, PATs, bearer <!-- tck: docker-auth@1/host-only -->
  tokens or refresh tokens through this grant. Docker-compatible clients
  receive usable placeholder credentials containing only admitted logins.
- **MUST NOT** expose host credential sources, helper installations or <!-- tck: docker-auth@1/source-private -->
  private interception keys through this grant. Public trust material may
  be supplied.
- **MUST** keep issued authentication tokens outside the sandbox and <!-- tck: docker-auth@1/token-mediation -->
  return opaque placeholders in client-visible token fields. Headers,
  extra fields, errors and redirects preserve this boundary. Unsupported
  exchanges fail closed. Token acquisition, response recovery and renewal
  strategies are runtime choices.
- **MUST** confine bearer substitution to admitted HTTPS requests to <!-- tck: docker-auth@1/bearer-scope -->
  the corresponding registry API, subject to network policy and applicable
  restrictions. Unknown or withdrawn placeholders fail closed. Renewal
  preserves credential confinement and cannot extend an issuer's expired
  authorization without obtaining new authorization.
- **MUST NOT** expose credential-bearing redirect URLs or upload state. <!-- tck: docker-auth@1/redirect-confinement -->
  Each resolved destination remains subject to network policy and
  applicable restrictions. Cross-host requests never carry the registry
  bearer or login. Opaque locations may represent credential-bearing URLs.
- **MUST** provide placeholder-backed authentication before install <!-- tck: docker-auth@1/lifetime -->
  hooks execute and during runtime, withdrawing run-scoped placeholders
  when the run ends. Source changes do not promise immediate revocation
  of previously issued registry tokens.
- **MUST NOT** grant network access, install Docker, start a daemon or <!-- tck: docker-auth@1/no-extra-access -->
  share the host daemon. Tooling, a daemon when needed, and egress grants
  are separate needs.

This contract concerns authentication and credential confinement, not
HTTP transport or upload-body conformance. It does not restrict the
underlying credential's repository/action authority or prevent disclosure
of accessible image content. Prefer read-only, repository-scoped bindings.
Independently supplied guest credentials and secrets embedded in image
content are outside this guarantee.

## Composition

Identical entries collapse; required wins over optional. Distinct entries
retain their registry bounds and optionality. Overlapping registries share
the runtime-selected authentication binding for that run. Endpoint
selection is runtime state. Config changes are conservatively treated as
new permission requests in the runtime-service surface.
