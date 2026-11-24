# `com.docker.sandbox/agent-sessions@2`

How to run the workload's agent headlessly inside the sandbox. This
capability describes commands to run a prompt in a new, a named, or the
most recent session, and to list past sessions.

- **Shape**: singleton — the commands describe the workload's agent —
  and exclusive with [`agent-sessions@1`](agent-sessions@1.md).
- **Permission surface**: **no**.

`@2` keeps `@1`'s fields. Its `resume` and `continue` run one prompt in
a named or the most recent session, where `@1`'s reopen one without a
prompt.

[agent-interactive-sessions@1](agent-interactive-sessions@1.md) describes
the workload's terminal commands. An agent Kit with both headless and
interactive modes declares both capabilities. A headless-only agent
declares `agent-sessions@2`. A `list` declared on both capabilities is
the same command, and validation rejects a mismatch.

## Config

```yaml
- type: com.docker.sandbox/agent-sessions@2
  config:
    prompt: [-p, "{{.Prompt}}"]
    resume: [-p, --resume, "{{.SessionID}}", "{{.Prompt}}"]
    continue: [-p, --continue, "{{.Prompt}}"]
    list: [sh, -c, "claude-sessions --format ids"]
```

| Field | Type | Rules |
|---|---|---|
| `prompt` | list\<string\> | Argv **tail** appended to the workload's launch command; runs one prompt headlessly in a new session. MUST reference `{{.Prompt}}`. | <!-- tck: agent-sessions@2/prompt-placeholder-required -->
| `resume` | list\<string\> | Argv tail; runs one prompt headlessly in a named session. MUST reference `{{.SessionID}}`. | <!-- tck: agent-sessions@2/session-id-placeholder-required -->
| `continue` | list\<string\> | Argv tail; runs one prompt headlessly in the most recent session. |
| `list` | string \| list | A **complete command** whose stdout enumerates resumable session ids, one per line, most recent first. A string runs via `sh -c`; a list supplies exec argv. |

`resume` and `continue` **MUST** also reference `{{.Prompt}}`. With no <!-- tck: agent-sessions@2/resume-continue-prompt-placeholder-required -->
person at the terminal, a headless invocation is one prompt: reopening a
session with nothing to say to it is not an operation, and a tail
without the prompt would run the agent with the caller's input silently
discarded. [agent-interactive-sessions@1](agent-interactive-sessions@1.md)
is where a session is reopened without one.

Every verb is optional. An absent verb means the agent has no such
operation. A declaration with no verbs is invalid, and an empty tail
reads as absent, as in `@1`.

## Launch configuration

Each headless invocation **MUST** use the workload's launch argv <!-- tck: agent-sessions@2/headless-from-launch-argv -->
(image `Entrypoint` + `Cmd`) followed by the verb's tail, with
placeholders substituted. The tail appends in the same way as
user-supplied arguments.

`list` **MUST** run as its own complete command, with stdout parsed as <!-- tck: agent-sessions@2/list-parses-stdout -->
resumable session ids, one per line, most recent first. These ids feed
`resume` verbatim.

An absent verb **MUST** be surfaced as "operation unsupported". <!-- tck: agent-sessions@2/absent-verb-unsupported -->
Only declared verbs supply invocation arguments.

A host **MUST** refuse to invoke `prompt`, `resume`, or `continue` <!-- tck: agent-sessions@2/prompt-required-at-invocation -->
without a non-empty prompt from the caller, reporting an error rather
than substituting an empty `{{.Prompt}}`. Every headless verb exists to
deliver a prompt, so an invocation without one has nothing to run, and
what an agent does with an empty argument is its own: some fail, some
wait on stdin.

The declaration **MUST NOT** contribute to the permission surface; it <!-- tck: agent-sessions@2/declaration-grants-nothing -->
only describes how to launch the agent already installed in the sandbox.

### Placeholders

`{{.Prompt}}` and `{{.SessionID}}` carry the caller's prompt and session
id. They may appear inside a larger token (`--prompt={{.Prompt}}`).
The `{{.X}}` shape matches the [credential-file](credential@1.md)
placeholders.

Placeholder substitution **MUST** insert raw caller values into argv <!-- tck: agent-sessions@2/raw-value-substitution -->
elements, preserving each element as one argument. The tail is exec
argv, so values are inserted without shell re-quoting.

## Composition

Composition retains a sole declaration and collapses identical
restatements. Differing declarations conflict, whether contributed by
workloads or mixins, an `@1` and an `@2` included. The resulting
commands describe the workload's agent
([§9.5](../../SPEC-v3.md#95-merging-a-set)).
