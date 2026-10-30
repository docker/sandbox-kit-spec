# `com.docker.sandbox/mcp-server@1`

Declares a [Model Context Protocol](https://modelcontextprotocol.io/)
(MCP) server that a sandbox runs, and how an MCP client reaches it. A
Kit declaring one is started as a sandbox like any other. The host then
connects to the server inside it and, when asked, serves it to another
sandbox's MCP gateway or to MCP clients on the host machine.

- **Shape**: instance-shaped, keyed on `name`. Workloads and mixins may
  declare it, so an agent Kit can carry servers of its own and a mixin
  can bring one into whatever workload it joins.
- **Permission surface**: **no**. The entry says how a client reaches a
  server already inside the sandbox. What the server can reach is the
  sandbox's own `network-policy` and `credential@1` entries, which
  project as they do for any Kit.

An entry starts its server one of two ways:

- **The workload's launch.** An entry without `command` says the
  sandbox's launch is the server: §8's entrypoint plus cmd, with the
  environment, user, and working directory the Kit already states. A
  Kit packaging one server needs nothing else, and `arguments` lets the
  installer configure it the way `docker run <image> <args>` would.
- **A command of its own.** An entry with `command` starts the server
  inside the sandbox beside whatever the launch is, the way
  [acp@1](acp@1.md) starts an adapter. Every mixin entry is one, since a
  mixin's entrypoint never takes effect (§10), and so is every entry in
  an `sbx@1` Kit, whose launch is the agent.

## Config

A Kit whose launch is a stdio server:

```yaml
- type: com.docker.sandbox/mcp-server@1
  config:
    name: docs
    transport: stdio
```

The same, configured on its command line, with the installer choosing
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

A streamable-HTTP server started by its own command, as an agent Kit or
a mixin would declare it:

```yaml
- type: com.docker.sandbox/mcp-server@1
  optional: true
  config:
    name: search
    transport: streamable-http
    port: 8080
    path: /mcp
    command: [search-mcp, serve, --port, "8080"]
```

| Field | Type | Rules |
|---|---|---|
| `name` | string | Required. The name a client reaches the server under: one lowercase DNS label (`[a-z0-9]`, inner hyphens, at most 63 characters). |
| `transport` | string | Required. `stdio` or `streamable-http`. |
| `port` | integer | Required for `streamable-http`, forbidden for `stdio`. The port inside the sandbox, 1-65535, the endpoint listens on. |
| `path` | string | Optional for `streamable-http`, forbidden for `stdio`; defaults to `/mcp`. An absolute URL path built from RFC 3986 path characters, with `%` only as a two-hex-digit escape: no query, fragment, or whitespace. |
| `command` | string \| list\<string\> | Optional. A nonblank **complete command** that starts the server inside the sandbox. A string runs via `sh -c`; a list supplies exec argv with a nonblank executable. Omit it to run the workload's launch. |
| `arguments` | list\<string\> | Optional, and only without `command`. The argv that replaces the launch's cmd. Non-empty when present (omit it to run the cmd); no element contains NUL. An element is passed verbatim, as one argv entry. |

`arguments` is the registry formats' command line. The MCP Registry's
`server.json` places `packageArguments` after the image and Docker's
MCP catalog states a `command` for it, and both mean what
`docker run <image> <args>` means: the entrypoint stays and the cmd is
replaced. An entry with `command` has no cmd to replace and states its
whole argv there instead. An element is one argv entry whatever it
resolves to, so an arg resolving to the empty string yields an empty
entry rather than none. Spell an optional value into its flag
(`--root=${{ kit.args.root }}`) when the server must not see a bare
empty argument.

A whole-value arg reference adopts its value's type when it expands
(§6), so an element of an expanded or set-pinned descriptor may be a
number or a boolean; it is passed as its spelling, which adoption
guarantees is the value supplied. Validation cannot tell such an
element from an authored literal, so it accepts both. Quote numeric
and boolean elements when authoring: unquoted, YAML decodes `1.0` and
`0x10` as numbers, and they reach the server as `1` and `16`.

The MCP specification's deprecated HTTP+SSE transport is not admitted.
A version of this capability that accepted it could not later drop it.

The config **MUST** satisfy the field rules above; unknown fields, null <!-- tck: mcp-server@1/config-valid -->
values, empty commands, NUL in command or argument strings,
transport-inapplicable fields, and `arguments` beside `command` are
invalid.

A declaration block **MUST NOT** contain two entries with the same <!-- tck: mcp-server@1/name-unique -->
`name`, even if they start different servers. The name is how a client
tells them apart.

`name`, `transport`, `port`, and `path` **MUST** be literal. A host <!-- tck: mcp-server@1/literal -->
lists and routes on them from the published artifact, in a catalog or
at a connect prompt, before any sandbox resolves its args. `command`
and `arguments` configure how the server starts, so they may reference
`${{ kit.args.* }}` and `${{ kit.env.* }}` as any create-phase value
can (§6.1).

An entry without `command` **MUST** be declared by a `kind: workload` <!-- tck: mcp-server@1/launch-entry -->
Kit that does not declare `sbx@1`, at most once per declaration block
counting group members. A sandbox has one launch; a mixin's launch
never takes effect; and `sbx@1` reads the launch as the agent's.

A Kit can declare the entry `optional`. A host that does not implement
this capability then skips it under §7.3 and starts the sandbox
without serving the server, which suits an agent Kit's extra servers.
A Kit that exists to be a server is better left required, so such a
host refuses it instead of starting a sandbox nothing reaches.

## Server guarantees

A Kit declaring this capability promises the following behavior of the
server the entry starts.

- With `stdio`, it **MUST** exchange MCP JSON-RPC messages, one per <!-- tck: mcp-server@1/stdio-stream -->
  line, on stdin and stdout. Stdout carries protocol messages only; logs
  go to stderr. Stdin EOF ends the server.
- With `streamable-http`, it **MUST** serve the MCP streamable-HTTP <!-- tck: mcp-server@1/http-endpoint -->
  transport at `port` and `path` on every interface of the sandbox, not
  only loopback, because the host's connection need not originate
  inside the sandbox.

## Runtime behavior

A conforming host, or the runtime on its behalf:

- **MUST** create the sandbox as for any Kit, including its permission <!-- tck: mcp-server@1/sandbox -->
  gate, resolution, and create-phase args. The declaration changes how
  the host reaches the sandbox, not whether it is one.
- For an entry without `command`, **MUST** run the server as the <!-- tck: mcp-server@1/launch -->
  sandbox's launch: entrypoint plus cmd as §8 states, with the effective
  `arguments` in place of the cmd, each element one argv entry, never
  re-split or passed through a shell.
- For an entry with `command`, **MUST** start it inside the sandbox as <!-- tck: mcp-server@1/command-launch -->
  the workload's user, with the working directory, environment, and
  credential injection an agent session receives, one process per
  connection for `stdio` and one per sandbox for `streamable-http`.
- With `stdio`, **MUST** connect its MCP client to the server's stdin <!-- tck: mcp-server@1/stdio-connect -->
  and stdout and treat stderr as logs, never as protocol.
- With `streamable-http`, **MUST** connect its MCP client to `port` and <!-- tck: mcp-server@1/http-connect -->
  `path` inside the sandbox. The entry publishes nothing; a
  [port@1](port@1.md) entry for the same port is a separate grant.
- **MUST NOT** advertise the server's tools until its MCP `initialize` <!-- tck: mcp-server@1/ready-before-advertised -->
  exchange has completed.
- **MUST** serve a server outside its sandbox — to another sandbox's MCP <!-- tck: mcp-server@1/serve-explicit -->
  gateway or to an MCP client on the host machine — only on a host
  request that names the sandbox and the server's `name`. Declaring the
  entry never does so by itself.
- **MUST** serve it under `name`, refusing a gateway that already <!-- tck: mcp-server@1/served-name -->
  serves that name rather than shadow either server.
- **MUST NOT** extend the consumer's filesystem, workspace, environment, <!-- tck: mcp-server@1/own-boundary -->
  network policy, or credentials to the server's sandbox, nor the
  server sandbox's to the consumer. Each side keeps its own boundary;
  only MCP messages cross it.
- **MUST** stop a command entry's process, and stop serving the server, <!-- tck: mcp-server@1/lifetime -->
  when its sandbox stops: stdin EOF for `stdio`, then `SIGTERM`, then
  `SIGKILL` once a bounded grace period passes.
- **MUST NOT** treat the declaration as a permission; the server's reach <!-- tck: mcp-server@1/declaration-grants-nothing -->
  is the sandbox's network and credential entries.

A gateway may multiplex several agent sessions onto one server
connection. A server that keeps per-client state should key it on the
MCP session, not on its process.

## Composition

Entries **MUST** union, keyed on `name`. Different configs for one name <!-- tck: mcp-server@1/composition -->
conflict, whether supplied by workloads, mixins, or both. An identical
restatement is the same entry: comparison uses decoded commands and the
default HTTP path, so an omitted `path` equals `/mcp`. As with other
capability entries, a required contribution makes the merged entry
required, and display metadata does not determine identity. A set
carries the entries of the Kits it lists; only its workload can
contribute an entry without `command`, so the merged descriptor has at
most one.

## Relationship to other capabilities

[acp@1](acp@1.md) requires an adapter to honor MCP servers a client
passes in `session/new`. Those are the client's choice for one session;
a server this capability declares is the Kit's, and serving it beyond
its sandbox is the host's choice. A client may pass a gateway serving it
in `session/new` like any other server.

A server runs with its own sandbox's filesystem and workspace, not its
consumer's. A server that needs the consumer's workspace — repository
search, Git history, file tools — is outside this version's contract;
granting it is a permission-surface question a later version answers
explicitly.
