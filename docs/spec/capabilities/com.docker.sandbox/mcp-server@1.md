# `com.docker.sandbox/mcp-server@1`

Declares that a workload's image is a
[Model Context Protocol](https://modelcontextprotocol.io/) (MCP) server.
A host never builds a sandbox from such a Kit. It attaches the Kit to an
existing sandbox's MCP gateway: it starts the image's own entrypoint
beside the sandbox and serves the server's tools to every agent session
that reaches the gateway.

- **Shape**: singleton, workload-only. Never optional, never a group
  member.
- **Permission surface**: **no**. The entry says how the gateway reaches
  the server. What the server itself can reach is its own
  `network-policy` and `credential@1` entries, which project as they do
  for any workload.

The image config is the launch contract, as §8 states for every
workload: entrypoint, cmd, env, user, and workdir start the server, and
the descriptor duplicates none of them. This capability adds only what
an image config has no slot for: which transport the entrypoint speaks;
for HTTP, which port and path; and, optionally, installer-configurable
arguments that replace the cmd.

## Config

A stdio server, reached on its standard streams:

```yaml
- type: com.docker.sandbox/mcp-server@1
  config:
    name: docs
    transport: stdio
```

A streamable-HTTP server, reached at a port and path inside its own
container:

```yaml
- type: com.docker.sandbox/mcp-server@1
  config:
    name: docs
    transport: streamable-http
    port: 8080
    path: /mcp
```

A server configured on its command line, with the installer choosing
the directory it serves:

```yaml
args:
  root:
    default: /data
    description: Directory the server exposes
capabilities:
  - type: com.docker.sandbox/mcp-server@1
    config:
      name: files
      transport: stdio
      arguments: ["--read-only", "--root=${{ kit.args.root }}"]
```

| Field | Type | Rules |
|---|---|---|
| `name` | string | Required. The name the gateway serves the server under: one lowercase DNS label (`[a-z0-9]`, inner hyphens, at most 63 characters). |
| `transport` | string | Required. `stdio` or `streamable-http`. |
| `port` | integer | Required for `streamable-http`, forbidden for `stdio`. The container port, 1-65535, the endpoint listens on. |
| `path` | string | Optional for `streamable-http`, forbidden for `stdio`; defaults to `/mcp`. An absolute URL path built from RFC 3986 path characters, with `%` only as a two-hex-digit escape: no query, fragment, or whitespace. |
| `arguments` | list\<string\> | Optional. The argv the entrypoint runs with in place of the image's cmd. Non-empty when present (omit it to run the cmd); no element contains NUL. An element is passed verbatim, as one argv entry. |

`arguments` is the registry formats' command line. The MCP Registry's
`server.json` places `packageArguments` after the image and Docker's
MCP catalog states a `command` for it, and both mean what
`docker run <image> <args>` means: the entrypoint stays and the cmd is
replaced. An element is one argv entry whatever it resolves to, so an
arg resolving to the empty string yields an empty entry rather than
none. Spell an optional value into its flag
(`--root=${{ kit.args.root }}`) when the entrypoint must not see a bare
empty argument.

A whole-value arg reference adopts its value's type when it expands
(§6), so an element of an expanded or set-pinned descriptor may be a
number or a boolean; it is passed as its spelling, which adoption
guarantees is the value supplied. Validation cannot tell such an
element from an authored literal, so it accepts both. Quote numeric
and boolean elements when authoring: unquoted, YAML decodes `1.0` and
`0x10` as numbers, and they reach the entrypoint as `1` and `16`.

The MCP specification's deprecated HTTP+SSE transport is not admitted.
A version of this capability that accepted it could not later drop it.

The entry **MUST** be declared by a `kind: workload` Kit. <!-- tck: mcp-server@1/workload-only -->
A mixin's layers overlay another image, so its entrypoint is never the
one a host would start. A set whose listed Kits include a server derives
`workload` from it, as §9.5 describes.

The entry **MUST NOT** be `optional` or a group member. <!-- tck: mcp-server@1/required -->
A host that skipped it under §7.3 would start the server as a sandbox,
and whether the image is a server is not an alternative a host selects.

The config **MUST** satisfy the field rules above; unknown fields, null <!-- tck: mcp-server@1/config-valid -->
values, and transport-inapplicable fields are invalid.

Config values **MUST** be literal, with one exception: `arguments` <!-- tck: mcp-server@1/literal -->
elements may reference `${{ kit.args.* }}`. No field references
`${{ kit.env.* }}`. The endpoint fields — `name`, `transport`, `port`,
and `path` — describe what the image's entrypoint serves, which no
installer value can change, and a gateway reads them from the published
artifact before anything is attached. `arguments` is the installer's
configuration of that entrypoint, so it takes create-phase args; the
container environment is one the entrypoint already reads for itself.

A Kit declaring this capability **MUST NOT** declare any other <!-- tck: mcp-server@1/server-only -->
well-known type than `network-policy@1`, `network-policy@2`,
`credential@1`, and `resources@1`, whether ordinary or in a group. Those
bound the server's own container. Every other well-known type describes
a sandbox: an agent, its sessions, its hooks, its filesystem, or its
lifetime. Types outside this specification are not judged.

Only the `runtime` phase applies. A server Kit has no install hooks, so
install-phase network and credential entries never take effect. The
§3.5 platform floor (`bash`, the `agent` user, `git`) exists for agent
launch and does not apply either: a distroless server image is fine.

## Server guarantees

A Kit declaring this capability promises the following behavior of its
image's entrypoint.

- With `stdio`, it **MUST** exchange MCP JSON-RPC messages, one per <!-- tck: mcp-server@1/stdio-stream -->
  line, on stdin and stdout. Stdout carries protocol messages only; logs
  go to stderr. Stdin EOF ends the server.
- With `streamable-http`, it **MUST** serve the MCP streamable-HTTP <!-- tck: mcp-server@1/http-endpoint -->
  transport at `port` and `path` on every interface of its container,
  not only loopback, which the gateway does not share.

## Runtime behavior

A conforming host, or the runtime on its behalf:

- **MUST** refuse to create a sandbox whose composition includes a Kit <!-- tck: mcp-server@1/not-a-sandbox -->
  declaring this capability, whether or not the runtime claims it. A
  runtime that does not claim it refuses under §7.3 already; one that
  claims it attaches such a Kit and never boots it as a sandbox.
- **MUST** attach a server only on a host request that names both the <!-- tck: mcp-server@1/attach-explicit -->
  Kit and the sandbox, and **MUST** present the server Kit's permission
  surface for approval first, as §7.4 does for a create. A sandbox's own
  composition never attaches a server.
- **MUST** resolve the server Kit's create-phase args for each <!-- tck: mcp-server@1/attach-args -->
  attachment as §6 does at create: the attach request supplies their
  values, a missing `required` value or one failing its `enum` or
  `pattern` refuses the attachment, and the effective descriptor is
  validated strictly before the server starts. `env:` args reach the
  server's environment, and the recorded values reproduce the
  attachment.
- **MUST** start the server from the image config — entrypoint plus <!-- tck: mcp-server@1/launch -->
  cmd, with its env, user, and workdir — in a container of its own, one
  process per attachment. With `arguments`, the entrypoint runs with the
  effective `arguments` in place of the cmd, each element one argv
  entry, never re-split or passed through a shell.
- With `stdio`, **MUST** connect the gateway to the process's stdin and <!-- tck: mcp-server@1/stdio-connect -->
  stdout and treat stderr as logs, never as protocol.
- With `streamable-http`, **MUST** connect the gateway to `port` and <!-- tck: mcp-server@1/http-connect -->
  `path` inside the server's container, and **MUST NOT** publish that
  port to the host network or to the sandbox.
- **MUST NOT** advertise the server's tools until its MCP `initialize` <!-- tck: mcp-server@1/ready-before-advertised -->
  exchange has completed.
- **MUST** serve the server through the sandbox's MCP gateway under <!-- tck: mcp-server@1/gateway-name -->
  `name`, and **MUST** refuse an attachment whose `name` is already
  attached to that sandbox rather than shadow either server.
- **MUST NOT** give the server the sandbox's filesystem, workspace, <!-- tck: mcp-server@1/isolated -->
  environment, or credentials. It runs beside the sandbox, not in it.
- **MUST** confine the server's egress to its own runtime-phase network <!-- tck: mcp-server@1/own-boundary -->
  policy and present its own credentials as `credential@1` specifies,
  at its own boundary. A server Kit with no network policy gets no
  egress. The sandbox's grants do not extend to the server, nor the
  server's to the sandbox.
- **MUST** stop the server when the sandbox stops or the attachment is <!-- tck: mcp-server@1/lifetime -->
  removed: stdin EOF for `stdio`, then `SIGTERM`, then `SIGKILL` once a
  bounded grace period passes.
- **MUST NOT** treat the declaration as a permission; the server's reach <!-- tck: mcp-server@1/declaration-grants-nothing -->
  is its network and credential entries.

The gateway may multiplex several agent sessions onto one server
connection. A server that keeps per-client state should key it on the
MCP session, not on its process.

## Composition

The entry **MUST** compose as a singleton: one author per merged <!-- tck: mcp-server@1/composition -->
descriptor, with an identical restatement collapsing. A set may list a
server Kit with mixins that add content to its image; the merged
descriptor is then judged by the rules above, so a mixin contributing a
sandbox-only type is refused. A set cannot carry a server onto an agent:
both are workloads, and a composition has exactly one.

## Relationship to other capabilities

[acp@1](acp@1.md) requires an adapter to honor MCP servers a client
passes in `session/new`. Those are the client's choice for one session;
a server attached through this capability is the host's choice for the
whole sandbox. Both reach the agent, and a client may pass the gateway
itself.

The server sees nothing of the sandbox. A server that needs the
workspace — repository search, Git history, file tools — is outside
this version's contract; granting it is a permission-surface question a
later version answers explicitly.
