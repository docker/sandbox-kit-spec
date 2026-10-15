# `com.docker.sandbox/acp@1`

How to run an [Agent Client Protocol](https://agentclientprotocol.com/)
(ACP) agent inside the sandbox. This capability describes the launch
command, its environment, and the protocol major version.

- **Shape**: instance-shaped, keyed on the normalized `agent` name.
  Workloads and mixins may declare it. The agent may be provided by
  another composed Kit.
- **Permission surface**: **no**.

## Config

```yaml
- type: com.docker.sandbox/acp@1
  optional: true
  description: Run Claude Code as an ACP agent
  config:
    agent: claude
    command: [claude-agent-acp]
    env:
      CLAUDE_CODE_EXECUTABLE: /usr/local/bin/claude
    protocolVersion: 1
```

| Field | Type | Rules |
|---|---|---|
| `agent` | string | Required. An unversioned capability name from the sandbox's composed `provides`, using the naming and namespace rules of §5.1 (`claude@2.1.292` names `claude`). Bare and default-namespace-qualified spellings identify the same agent. |
| `command` | string \| list\<string\> | Required. A nonblank **complete command** to run the ACP agent on stdio without a TTY, never an argv tail. A string runs via `sh -c`; a list supplies exec argv with a nonblank executable. |
| `env` | map\<string, string\> | Optional. Extra environment for this process, overlaid on the agent session environment. Keys are environment variable names. Values may be empty. |
| `protocolVersion` | integer | Optional, defaults to `1`. The ACP major version the command is configured to speak, not a minimum or a package release number. Positive. |

Kit authors **SHOULD** publish the entry with `optional: true`, so a host <!-- tck: acp@1/optional-for-compatibility -->
that does not recognize this capability skips it under §7.3 instead of
refusing the Kit. An entry in an optional group inherits the group's
optionality.

The config **MUST** satisfy the field rules above; unknown fields, null <!-- tck: acp@1/config-valid -->
values, empty commands, versioned agent names, and NUL in command or
environment strings are invalid.

A declaration block **MUST NOT** contain two entries naming the same <!-- tck: acp@1/agent-unique -->
normalized agent, even if their commands differ.

## Launch configuration

`command` runs inside the sandbox as the workload's user, with the
working directory, environment, and credential injection an agent
session receives. `env` overlays that environment for this process
only. The invocation uses stdin and stdout without a terminal, with
stderr separate.

`agent` identifies the agent in the sandbox's composed `provides`, not
just the declaring Kit's `provides`. An entry whose agent is absent is
not applicable to that sandbox.

The declaration **MUST NOT** contribute to the permission surface; it <!-- tck: acp@1/declaration-grants-nothing -->
only describes how to launch an agent already installed in the sandbox.

## Composition

Entries **MUST** union, keyed on the normalized agent name. <!-- tck: acp@1/composition -->
Different configs for one agent conflict, whether supplied by workloads,
mixins, or both. An identical restatement is the same entry: comparison
uses decoded argv, normalized agent names, and the default protocol
version; omitted and empty `env` maps are equal. As with other capability
entries, a required contribution makes the merged entry required, and
display metadata does not determine identity. A set carries the entries
of the Kits it lists.
