# `com.docker.sandbox/agent-sessions@1`

The workload's session control surface: the argv shapes a harness uses to
drive the agent — run one prompt headlessly, list past sessions, resume or
continue one. Not a grant: the host consumes it to operate the agent, the
way it consumes the image config's entrypoint.

This is the headless side of an agent's session surface; its interactive
sibling is [agent-interactive-sessions@1](agent-interactive-sessions@1.md),
which carries the TUI verbs a human-facing host launches. An agent Kit
whose CLI has both a headless and an interactive mode declares **both**
capabilities: agent-sessions@1 for the headless verbs a harness drives,
agent-interactive-sessions@1 for the TUI verbs a human-facing host
launches. A non-interactive-only agent declares only agent-sessions@1.
A `list` declared on both capabilities is the same command in both, and
validation rejects a mismatch.

- **Shape**: singleton. Workload Kits in practice — the agent the verbs
  drive is the workload's.
- **Permission surface**: **no** — a declaration about the workload's own
  CLI, on the entrypoint's trust plane.

## Config

```yaml
- type: com.docker.sandbox/agent-sessions@1
  config:
    prompt: [-p, "{{.Prompt}}"]            # run one prompt non-interactively
    resume: [--resume, "{{.SessionID}}"]   # reopen a named session
    continue: [--continue]                 # reopen the most recent session
    list:                                  # enumerate resumable session ids
      - sh
      - -c
      - claude-sessions --format ids
```

| Field | Type | Rules |
|---|---|---|
| `prompt` | list\<string\> | Argv **tail** appended to the workload's launch command. MUST reference `{{.Prompt}}`. | <!-- tck: agent-sessions@1/prompt-placeholder-required -->
| `resume` | list\<string\> | Argv tail. MUST reference `{{.SessionID}}`. | <!-- tck: agent-sessions@1/session-id-placeholder-required -->
| `continue` | list\<string\> | Argv tail; no placeholder. |
| `list` | string \| list | A **complete command** (not a tail) whose stdout enumerates resumable session ids, one per line, most recent first. |

Every verb is optional — an absent verb means the agent has no such
operation — but a declaration with no verbs at all says nothing and is
invalid.

### Placeholders

`{{.Prompt}}` and `{{.SessionID}}` are substituted by the host before
execution, and may ride inside a larger token (`--prompt={{.Prompt}}`).
The `{{.X}}` shape matches the [credential-file](credential@1.md)
placeholders — one substitution vocabulary across the grammar. The
placeholder is the verb's whole point: a prompt verb that never receives
the prompt would run the agent with the caller's input silently discarded,
so the references are validation requirements.

## Runtime behavior

A conforming runtime (or harness):

- **MUST** build the headless invocation as the workload's launch argv <!-- tck: agent-sessions@1/headless-from-launch-argv -->
  (image `Entrypoint` + `Cmd`) plus the verb's tail, with placeholders
  substituted — the same way user-supplied args append. Verb tails never
  replace the launch command.
- **MUST** substitute the raw caller values (no shell re-quoting into the <!-- tck: agent-sessions@1/raw-value-substitution -->
  argv elements — the tail is exec argv, not a shell string).
- **MUST** run `list` as its own complete command and parse stdout as ids, <!-- tck: agent-sessions@1/list-parses-stdout -->
  one per line, most recent first; ids feed `resume` verbatim.
- **MUST** treat an absent verb as "operation unsupported" and surface <!-- tck: agent-sessions@1/absent-verb-unsupported -->
  that, rather than improvising flags.
- **MUST NOT** treat the declaration as a permission: it grants nothing; <!-- tck: agent-sessions@1/declaration-grants-nothing -->
  it teaches the host how to drive what the workload already runs.

## Composition

The workload Kit's declaration governs. A mixin declaring agent-sessions
has nothing to drive; composition keeps the workload's entry.
