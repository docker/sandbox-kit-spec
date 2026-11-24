# `com.docker.sandbox/agent-sessions@1`

How to run the workload's agent headlessly inside the sandbox. This
capability describes commands to run a prompt, resume or continue a
session, and list past sessions.

- **Shape**: singleton. The commands describe the workload's agent.
- **Permission surface**: **no**.

[agent-interactive-sessions@1](agent-interactive-sessions@1.md) describes
the workload's terminal commands. An agent Kit with both headless and
interactive modes declares both capabilities. A headless-only agent
declares `agent-sessions@1`. A `list` declared on both capabilities is
the same command, and validation rejects a mismatch.

## Config

```yaml
- type: com.docker.sandbox/agent-sessions@1
  config:
    prompt: [-p, "{{.Prompt}}"]
    resume: [--resume, "{{.SessionID}}"]
    continue: [--continue]
    list: [sh, -c, "claude-sessions --format ids"]
```

| Field | Type | Rules |
|---|---|---|
| `prompt` | list\<string\> | Argv **tail** appended to the workload's launch command; runs one prompt headlessly. MUST reference `{{.Prompt}}`. | <!-- tck: agent-sessions@1/prompt-placeholder-required -->
| `resume` | list\<string\> | Argv tail; reopens a named session. MUST reference `{{.SessionID}}`. | <!-- tck: agent-sessions@1/session-id-placeholder-required -->
| `continue` | list\<string\> | Argv tail; reopens the most recent session. No placeholder. |
| `list` | string \| list | A **complete command** whose stdout enumerates resumable session ids, one per line, most recent first. A string runs via `sh -c`; a list supplies exec argv. |

Every verb is optional. An absent verb means the agent has no such
operation. A declaration with no verbs is invalid.

## Launch configuration

Each headless invocation **MUST** use the workload's launch argv <!-- tck: agent-sessions@1/headless-from-launch-argv -->
(image `Entrypoint` + `Cmd`) followed by the verb's tail, with
placeholders substituted. The tail appends in the same way as
user-supplied arguments.

`list` **MUST** run as its own complete command, with stdout parsed as <!-- tck: agent-sessions@1/list-parses-stdout -->
resumable session ids, one per line, most recent first. These ids feed
`resume` verbatim.

An absent verb **MUST** be surfaced as "operation unsupported". <!-- tck: agent-sessions@1/absent-verb-unsupported -->
Only declared verbs supply invocation arguments.

The declaration **MUST NOT** contribute to the permission surface; it <!-- tck: agent-sessions@1/declaration-grants-nothing -->
only describes how to launch the agent already installed in the sandbox.

### Placeholders

`{{.Prompt}}` and `{{.SessionID}}` carry the caller's prompt and session
id. They may appear inside a larger token (`--prompt={{.Prompt}}`).
The `{{.X}}` shape matches the [credential-file](credential@1.md)
placeholders.

Placeholder substitution **MUST** insert raw caller values into argv <!-- tck: agent-sessions@1/raw-value-substitution -->
elements, preserving each element as one argument. The tail is exec
argv, so values are inserted without shell re-quoting.

## Composition

Composition retains a sole declaration and collapses identical
restatements. Differing declarations conflict, whether contributed by
workloads or mixins. The resulting commands describe the workload's
agent ([§9.5](../../SPEC-v3.md#95-merging-a-set)).
