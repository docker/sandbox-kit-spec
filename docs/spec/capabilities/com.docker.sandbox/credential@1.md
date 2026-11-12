# `com.docker.sandbox/credential@1`

One service the workload authenticates to, and how the runtime presents
proof — never where the secret lives. The host's credential store is the
sole source; a Kit declares the need, the user's bindings answer it.

- **Shape**: instance — one entry may name one or both phases; overlapping
  (service, phase) pairs in the same declaration block are rejected.
- **Permission surface**: yes — the service name, per phase.

## Config

```yaml
- type: com.docker.sandbox/credential@1
  optional: true                       # entry-level: skip when unbound
  description: GitHub API access for gh
  config:
    service: github                    # REQUIRED: identifier in the host credential store
    phase: [install, runtime]          # REQUIRED: one phase or a list
    apiKey:                            # api-key presentation
      name: GH_TOKEN
      proxyManaged: true
      inject:
        - {domain: api.github.com, header: Authorization, format: "Bearer %s"}
        - {domain: github.com, scheme: basic, username: x-access-token}
    oauth:                             # OAuth presentation
      tokenEndpoint: {host: platform.claude.com, path: /v1/oauth/token}
      resourceHosts: [api.anthropic.com]
      sentinels:
        accessToken: sk-ant-oat01-proxy-managed
        refreshToken: sk-ant-ort01-proxy-managed
      credentialFile:
        path: ~/.claude/.credentials.json
        structure:
          claudeAiOauth:
            accessToken: "{{.AccessToken}}"
      responseFields: {accessToken: access_token, refreshToken: refresh_token, expiresIn: expires_in}
      passthrough: false
```

| Field | Type | Rules |
|---|---|---|
| `service` | string | REQUIRED. Lowercase-kebab name in the host credential store. |
| `phase` | string or list\<string\> | REQUIRED. `install`, `runtime`, or a non-empty list of distinct phases. The same credential configuration applies to every listed phase. |
| `apiKey` | object | conditional. At least one of `apiKey`/`oauth` MUST be declared. | <!-- tck: credential@1/one-of-apikey-oauth -->
| `apiKey.name` | string | In-container env var name. Empty or omitted with `inject` rules present declares an inject-only credential: outbound rewrites with no environment presence, not even a sentinel. At least one of `name`/`inject` MUST be declared. | <!-- tck: credential@1/name-or-inject -->
| `apiKey.proxyManaged` | bool | The real value stays on the host. A named key's variable carries a sentinel; an inject-only key has no in-container presence, and the boundary presents the real value outbound either way. |
| `apiKey.inject[]` | list | Outbound rewrite rules. `domain` REQUIRED and MUST appear in the network policy's allow list for every declared phase, whether the Kit declares [`@1`](network-policy@1.md) or [`@2`](network-policy@2.md). | <!-- tck: credential@1/inject-domain-in-allow -->
| `apiKey.inject[].header` / `format` | string | Header to set; `format` renders the value (e.g. `"Bearer %s"`). |
| `apiKey.inject[].scheme` / `username` | string | Non-header presentation, e.g. `basic` with `username` (credential as password). |
| `oauth.tokenEndpoint` | object | `host` REQUIRED when `tokenEndpoint` is set. |
| `oauth.resourceHosts` | list\<string\> | API hosts where the bearer is used. |
| `oauth.sentinels` | object | The placeholder access/refresh tokens rendered in-container. |
| `oauth.credentialFile` | object | Renders sentinels into a file the agent reads. `structure` is a declarative nested map, encoded after substitution — output is well-formed regardless of values. |
| `oauth.credentialFile.format` | string | Encoding of the substituted structure: `json` (default) or `toml`, for agents that read TOML credential files. Nested maps become TOML tables. |
| `oauth.responseFields` | object | Maps a provider's nonstandard token-response field names: `accessToken`, `refreshToken`, `expiresIn`. The refresh mapping also covers providers that reuse one field for both tokens. |
| `oauth.passthrough` | bool | Returns the real token to the container: a downgrade. |

`credentialFile.structure` leaf strings may reference `{{.AccessToken}}`
and `{{.RefreshToken}}` (strings), `{{.ExpiresAt}}` (a number),
`{{.Scopes}}` (an array), and `{{.PrimaryApiKey}}` (a string whose
enclosing key is omitted when no key is captured). Unknown placeholders are
errors. Each placeholder renders in the target encoding's own type.

Scalar phase declarations remain valid. The list form requires a reader
implementing this extension; older readers reject it.

## Runtime behavior

A conforming runtime:

- **MUST** resolve the credential from the host-side store keyed by <!-- tck: credential@1/resolved-from-host-store -->
  `service`. Host environment variables never auto-inject; a Kit cannot
  name where a secret lives, only what it needs.
- **MUST NOT** place the real secret in the container when `proxyManaged` <!-- tck: credential@1/secret-absent-in-sandbox -->
  or OAuth sentinels are in play: where the Kit names a variable or file,
  the container sees sentinel values there, and the boundary (proxy)
  substitutes the real credential on outbound requests matching the
  `inject` rules or `resourceHosts`.
- **MUST** intercept the OAuth `tokenEndpoint` and serve sentinel tokens, <!-- tck: credential@1/oauth-token-endpoint-intercepted -->
  refreshing host-side; `passthrough: true` is the explicit opt-out and a
  security downgrade a runtime MAY refuse.
- **MUST** scope by phase: an install-only credential is injectable during <!-- tck: credential@1/phase-scoped -->
  Install, including workspace preparation and install commands, and is
  revoked before Start. Runtime credentials cover Start through Stop,
  including startup hooks, and are unavailable during Install. When both
  phases are listed, the credential is available in both with the same
  configuration.
- **MUST** scope a bound, named, proxy-managed API key's sentinel to its <!-- tck: credential@1/sentinel-phase-scoped -->
  declared phases. This includes install hooks that declare its
  variable in `env`, the workload's initial environment, and later runtime
  commands: each receives a sentinel in a granted phase and none outside
  the granted phases. This is the environment-visible part of the broader
  phase boundary above; outbound injection and OAuth presentations remain
  subject to that boundary independently.
- **MUST** fail resolution when a **required** entry has no binding; an <!-- tck: credential@1/required-without-binding-fails -->
  **optional** entry with no binding is skipped and recorded, and the Kit
  runs unauthenticated.
- **SHOULD** set the `apiKey.name` env var to a sentinel (not empty) when <!-- tck: credential@1/sentinel-not-empty -->
  the credential is wired and the Kit names one, so Kit content can detect
  wiring without seeing the secret.
- **MUST** give an inject-only credential (no `name`) no environment <!-- tck: credential@1/inject-only-no-env -->
  presence at all: composing it adds no variable, sentinel or otherwise.

## Composition

Each listed phase participates independently in composition. Entries
union across the set. Two Kits declaring the same (service, phase) is a
composition conflict — one credential, one owner — even when one entry
uses a scalar and the other lists both phases.

## Gate

The service name is permission surface, per phase. A new service, or a
service moving between phases, widens.
