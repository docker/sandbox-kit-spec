# `com.docker.sandbox/mcp-registration@1`

Registers one remote MCP server with the sandbox. The runtime connects
to the server from outside the sandbox and offers its tools to the
agent, so the person running the sandbox registers nothing with the
runtime first and the agent holds no credential for the server. An
entry that names a `cross-app-access@1` grant is reached with that
grant's access token: the person's identity provider decides whether
the agent may use the server, and neither a consent screen nor a client
registration stands between the Kit and a working tool.

Where `cross-app-access@1` puts a token on requests the sandbox makes,
this capability has the runtime make the requests, as the client of a
server the agent sees only as tools.

- **Shape**: instance-shaped on `name`; `access` is optional.
- **Permission surface**: **yes**. Presence gives the agent the
  server's tools, over a connection the runtime makes outside the
  sandbox's network boundary.
- **Requires**: the `cross-app-access@1` entry that `access` names,
  declared beside this one. An entry without `access` requires nothing.

## Config

```yaml
capabilities:
  - type: com.docker.sandbox/workload-identity@1
  - type: com.docker.sandbox/network-policy@2
    config:
      runtime:
        allow:
          - hosts: [mcp.example.com]
  - type: com.docker.sandbox/cross-app-access@1
    config:
      name: todos
      issuer: https://auth.example.com
      inject:
        - domain: mcp.example.com
  - type: com.docker.sandbox/mcp-registration@1
    description: Offer the todo server's tools to the agent
    config:
      name: todos
      url: https://mcp.example.com/mcp
      access: todos
```

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | Identifies the server among those the agent is offered. `[A-Za-z0-9][A-Za-z0-9_.-]{0,63}`. |
| `url` | yes | The server's streamable HTTP endpoint: an `https` URL of at most 256 characters without userinfo, query or fragment. |
| `access` | no | `name` of the `cross-app-access@1` entry whose access token the runtime presents to the server. Absent reaches the server without a credential. |

- A stated `name` **MUST** match the grammar above or be a reference. <!-- tck: mcp-registration@1/name-grammar -->
- A stated `url` **MUST** be an `https` URL within the bounds above, or <!-- tck: mcp-registration@1/url-grammar -->
  a reference.
- The host of `url` **MUST** appear in the network policy's runtime <!-- tck: mcp-registration@1/url-within-allow -->
  allow list, matched exactly, or be covered by a bare wildcard. The
  runtime makes the connection, so the sandbox's network boundary never
  sees it; a server the sandbox could not reach itself is not one a Kit
  hands its agent.
- A stated `access` **MUST** name a `cross-app-access@1` entry declared <!-- tck: mcp-registration@1/access-declared -->
  beside the registration, or be a reference.
- The `url` of an entry stating `access` **MUST** be one that grant's <!-- tck: mcp-registration@1/access-serves-url -->
  token belongs on: its host is one of the grant's `inject[].domain`
  values on the default port, that inject rule presents the token as
  `Authorization: Bearer`, and the URL is the grant's `resource` when
  the grant states one. A Kit cannot point one application's token at
  another's endpoint.
- The entry **MUST NOT** state a field this page does not define. An <!-- tck: mcp-registration@1/fields-closed -->
  OAuth client, a client secret, request headers and tokens are not the
  Kit's to carry; a server that needs the person's own consent is one
  the person registers with the runtime.

## Runtime behavior

A conforming runtime:

- **MUST** offer the server's tools to the agent under the declared <!-- tck: mcp-registration@1/served -->
  `name`, without the person registering the server with the runtime.
- **MUST** connect to `url` itself, outside the sandbox, so that the <!-- tck: mcp-registration@1/connected-outside -->
  agent reaches the server only through the tools the runtime offers.
- **MUST** authenticate to a server whose entry states `access` with <!-- tck: mcp-registration@1/access-only -->
  the access token of the named grant and nothing else: no token the
  person obtained for the server another way, no client of the
  runtime's own, and no authorization flow offered to the agent.
- **MUST NOT** connect a server whose named grant yields no access <!-- tck: mcp-registration@1/no-token-not-connected -->
  token. The server is left out and reported, and returns once the
  identity provider allows the grant again.
- **MUST NOT** attach a credential to, or start an authorization flow <!-- tck: mcp-registration@1/anonymous -->
  for, a server whose entry states no `access`.
- **MUST** hold the server's tools to every control it applies to the <!-- tck: mcp-registration@1/policy-applies -->
  tools of a server the person registered: a registration that arrives
  in a Kit is not a way around the organization's tool policy.
- **MUST NOT** offer the server to a sandbox whose effective descriptor <!-- tck: mcp-registration@1/absent-without-entry -->
  does not carry this entry.
- **MUST** withdraw the registration when the sandbox is removed. <!-- tck: mcp-registration@1/removed-with-sandbox -->
- **MUST** refuse a required entry when it cannot serve MCP servers to <!-- tck: mcp-registration@1/unavailable-refuses-required -->
  the agent, when it cannot serve the declared `name` as this page
  requires, for instance because the person registered another server
  under it, or when the grant `access` names was refused or skipped. An
  optional entry is skipped and recorded.

The access token stays where `cross-app-access@1` keeps it: with the
runtime. What the server returns in a tool result is the server's
choice, as it is for any application a grant reaches.

## Composition

A workload, mixin or enclosing set can register a server. Entries
reconcile on `name`: identical restatements collapse and a required
declaration wins over optional ones; two entries with one `name` but a
different `url` or `access` conflict. Different names coexist, so a
workload can carry one registration per server.

## Gate

Adding the capability widens permission surface, even when optional: it
gives the agent tools backed by a connection the sandbox's network
boundary does not see. The entry contributes its type and config digest
to the runtime-service portion of the surface, so a change of `url` or
`access` stops for approval like any other config change.
