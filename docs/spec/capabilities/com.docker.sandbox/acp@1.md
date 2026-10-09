# `com.docker.sandbox/acp@1`

How to start an agent's [Agent Client Protocol](https://agentclientprotocol.com/)
(ACP) adapter inside the sandbox. The adapter speaks for the agent rather
than making a host infer session state from processes and argv. This is a
declaration on the entrypoint's trust plane; it grants no access.

- **Shape**: instance-shaped, keyed on the normalized `agent` name.
  Workloads and mixins may declare it, including an adapter mixin whose
  agent comes from another Kit.
- **Permission surface**: **no**.

ACP complements [agent-sessions@1](agent-sessions@1.md) and
[agent-interactive-sessions@1](agent-interactive-sessions@1.md). Those
remain the headless and terminal fallbacks for hosts without ACP support.
A mixin's agent can rely on ACP's negotiated session operations even
though the fallback capabilities describe only the workload's agent.

## Config

```yaml
- type: com.docker.sandbox/acp@1
  optional: true
  description: Drive Claude Code sessions over ACP
  config:
    agent: claude
    command: [claude-agent-acp]
    env:
      CLAUDE_CODE_EXECUTABLE: /usr/local/bin/claude
    protocolVersion: 1
```

| Field | Type | Rules |
|---|---|---|
| `agent` | string | Required. An unversioned capability name from `provides`, using the naming and namespace rules of §5.1 (`claude@2.1.292` names `claude`). Bare and default-namespace-qualified spellings identify the same agent. |
| `command` | string \| list\<string\> | Required. A nonblank **complete command**, never an argv tail. A string runs via `sh -c`; a list supplies exec argv with a nonblank executable. |
| `env` | map\<string, string\> | Optional. Extra environment for this adapter process; keys are environment variable names. Values may be empty. |
| `protocolVersion` | integer | Optional, defaults to `1`. Positive ACP major version, not a minimum. The host still negotiates at `initialize`. |

Kit authors **SHOULD** publish the entry with `optional: true`, so a host <!-- tck: acp@1/optional-for-compatibility -->
that does not implement ACP skips it under §7.3 instead of refusing the
Kit. An entry in an optional group inherits the group's optionality.

The config **MUST** satisfy the field rules above; unknown fields, null <!-- tck: acp@1/config-valid -->
values, empty commands, versioned agent names, and NUL in command or
environment strings are invalid.

A declaration block **MUST NOT** contain two entries naming the same <!-- tck: acp@1/agent-unique -->
normalized agent, even if their commands differ.

## Runtime behavior

A conforming host, or the runtime on its behalf:

- **MUST** start `command` inside the sandbox as the workload's user, <!-- tck: acp@1/sandbox-launch -->
  without a TTY, with the working directory, environment, and credential
  injection an agent session receives. `env` overlays that environment
  for this process only. Stdin and stdout stream full duplex without
  buffering whole turns; stderr remains separate.
- **MUST** use only entries whose normalized `agent` name is in the <!-- tck: acp@1/provided-agent -->
  sandbox's composed `provides`, ignoring entries whose agents are absent.
  A declaring adapter mixin need not itself provide the agent.
- **MUST** obtain the protocol version and advertised adapter capabilities <!-- tck: acp@1/negotiated-features -->
  from `initialize`, interpreting them according to that version. Session
  operations and MCP transports follow that version's baseline and
  capability advertisements; modes, models, and config options come from
  session creation or resumption responses and subsequent session updates.
  The descriptor does not advertise these features.
- **MUST NOT** proxy ACP `fs/*` or `terminal/*` client methods through <!-- tck: acp@1/no-host-io -->
  the host machine on the adapter's behalf. A host may offer neither.
- **MUST** identify sessions by normalized agent name and session id <!-- tck: acp@1/session-identity -->
  together; ids are unique only within one agent.
- **MUST NOT** drive one native session over ACP and the terminal or <!-- tck: acp@1/single-driver -->
  headless fallback simultaneously.
- **MUST NOT** treat the declaration as a permission; it teaches the <!-- tck: acp@1/declaration-grants-nothing -->
  host how to drive an agent already installed in the sandbox.

## Adapter guarantees

A Kit declaring this capability promises the following behavior of its
adapter, including when the adapter is the agent binary itself.

- **MUST** exchange JSON-RPC 2.0 messages, one per line, over stdin and <!-- tck: acp@1/transport -->
  stdout. Stdout carries protocol messages only; logs go to stderr.
- **MUST NOT** require an ACP authentication step when the Kit's <!-- tck: acp@1/noninteractive-auth -->
  `credential@1` requests have been satisfied (`authenticate` in ACP 1,
  `auth/login` in ACP 2).
- **MUST** create native harness sessions and return the harness's own <!-- tck: acp@1/native-sessions -->
  id from `session/new`. For the workload's agent, it is the id that
  `agent-sessions@1` lists and its `resume` accepts when those verbs are
  declared. Conversations can continue in either ACP or the terminal.
- **MUST** support native-session resumption using the negotiated version's <!-- tck: acp@1/load-session -->
  methods. With ACP 1, this means `session/load` and `loadSession: true`.
  With ACP 2, this means `session/resume`, including its history replay
  options, and the baseline `session/list` operation. Session listing
  and resume without replay are supported when those operations exist
  in the negotiated version.
- **MUST** perform file and shell work inside the sandbox when the host <!-- tck: acp@1/sandbox-io -->
  offers neither client filesystem nor terminal capabilities.
- **MUST** honor MCP servers passed in `session/new`, both stdio and <!-- tck: acp@1/mcp-servers -->
  HTTP transports.
- **MUST** start in the same permission posture as the Kit's interactive <!-- tck: acp@1/permission-posture -->
  entrypoint and expose stricter postures as session modes in ACP 1 or
  config options in ACP 2. A Kit bypassing sandbox approvals does not
  add edit approvals over ACP.
- **MUST** expose per-session model and reasoning-effort choices as <!-- tck: acp@1/model-effort -->
  session modes or config options where the harness and negotiated
  protocol support them.
- **MUST** report the launch of background shells and sub-agents as tool <!-- tck: acp@1/background-work -->
  calls through `session/update`. With ACP 1, this capability does not
  guarantee subsequent progress or completion updates for detached work;
  a negotiated extension can provide that guarantee. Completion of the
  launch tool call or foreground turn is not evidence that the detached
  work has ended.
- **MUST**, when ACP 2 is negotiated, report background shell output <!-- tck: acp@1/background-work-v2 -->
  through `terminal_output_chunk` or `terminal_update` and termination
  through `terminal_update.exitStatus`, linking each terminal to its
  launch tool call by `terminalId`. Sub-agent tool calls receive status
  updates until completion. These updates continue while foreground
  state is `idle`; the session stays alive while the work runs unless
  the host closes it. No client-specific extension is required.
- **MUST** cancel running turns, flush sessions to the harness's store, <!-- tck: acp@1/shutdown -->
  and exit within five seconds of stdin EOF or `SIGTERM`. Stored sessions
  remain resumable.
- **MUST** allow several adapter processes in one sandbox, each with <!-- tck: acp@1/concurrency -->
  several sessions, without corrupting the harness's session store.
- **MUST** retain the native session id in hook payloads when agent hooks <!-- tck: acp@1/hook-session-id -->
  fire for ACP sessions, so hosts can discard duplicate hook events.

## Composition

Entries **MUST** union, keyed on the normalized agent name. <!-- tck: acp@1/composition -->
Different configs for one agent conflict, whether supplied by workloads,
mixins, or both. An identical restatement is the same entry: comparison uses decoded argv,
normalized agent names, and the default protocol version; omitted and
empty `env` maps are equal. As with other capability entries, a required
contribution makes the merged entry required, and display metadata does
not determine identity. A set carries the entries of the Kits it lists.

An adapter that outlives a host connection, socket reattachment, and
runtime CLI shortcuts are outside this capability's contract.
[long-running@1](long-running@1.md) is the separate declaration for
session-independent sandbox lifetime.

ACP 1 is the baseline for existing adapters. Declaring `protocolVersion: 2`
requires an adapter that implements ACP 2; an adapter package's release
number does not identify its protocol version. The negotiated version
determines the protocol duties above. ACP 2's standard background terminal
updates are not implied by an ACP 1 declaration.
