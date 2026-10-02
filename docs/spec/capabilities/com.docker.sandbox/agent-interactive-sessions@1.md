# `com.docker.sandbox/agent-interactive-sessions@1`

The workload's interactive session control surface: the argv shapes a
human-facing host uses to put the agent's terminal UI in front of a
person — start a session, seed it with a prompt, reopen or pick a past
one. Not a grant: the host consumes it to operate the agent, the way it
consumes the image config's entrypoint.

This is the interactive sibling of
[agent-sessions@1](agent-sessions@1.md), which carries the headless verbs
a harness drives. An agent Kit whose CLI has both a headless and an
interactive mode declares **both** capabilities: agent-sessions@1 for the
headless verbs a harness drives, agent-interactive-sessions@1 for the TUI
verbs a human-facing host launches. An agent with no interactive mode
declares only agent-sessions@1, and one with no headless mode only this
capability.

- **Shape**: singleton. Workload Kits in practice — the agent the verbs
  drive is the workload's.
- **Permission surface**: **no** — a declaration about the workload's own
  CLI, on the entrypoint's trust plane.

## Config

```yaml
- type: com.docker.sandbox/agent-interactive-sessions@1
  config:
    prompt: ["{{.Prompt}}"]                # start a session seeded with a prompt
    resume: [--resume, "{{.SessionID}}"]   # reopen a named session
    continue: [--continue]                 # reopen the most recent session
    newSession: []                         # fresh session: the launch argv alone
    sessionPicker: [--resume]              # start on the agent's session picker
    list:                                  # enumerate resumable session ids
      - sh
      - -c
      - claude-sessions --format ids
```

| Field | Type | Rules |
|---|---|---|
| `prompt` | list\<string\> | Argv **tail** appended to the workload's launch command; starts an interactive session seeded with the prompt. MUST reference `{{.Prompt}}`. | <!-- tck: agent-interactive-sessions@1/prompt-placeholder-required -->
| `resume` | list\<string\> | Argv tail; reopens a named session interactively. MUST reference `{{.SessionID}}`. | <!-- tck: agent-interactive-sessions@1/session-id-placeholder-required -->
| `continue` | list\<string\> | Argv tail; reopens the most recent session interactively. No placeholder. |
| `newSession` | list\<string\> | Argv tail; starts a fresh interactive session with no prompt. No placeholder. Often `[]`. **Omitted, it defaults to the [lifecycle@1](lifecycle@1.md) interactive launch** — the launch argv plus lifecycle's `interactive` tail, or the launch argv alone when none is declared. |
| `sessionPicker` | list\<string\> | Argv tail; starts the agent on its own session picker. No placeholder. |
| `list` | string \| list | A **complete command** (not a tail) whose stdout enumerates resumable session ids, one per line, most recent first. The same type and meaning as agent-sessions@1's `list`: a Kit declaring both capabilities repeats the same command in both. |

Every verb is optional, but a declaration with no keys at all says
nothing and is invalid.

### Presence

For every argv-tail verb here except `newSession`, a **present** key means
the agent supports the operation and an **absent** key means it does not.
A present empty list means the launch argv alone: for most agents the
bare interactive launch *is* the launch argv, so `continue: []` or
`sessionPicker: []` are as meaningful as any other tail. This differs
deliberately from agent-sessions@1, where an empty tail reads as absent;
agent-sessions@1 is unchanged. A consumer therefore tells `[]` from an
omitted key, and anything that re-renders the declaration between
authoring and consumption has to keep an empty list (see
[Composition](#composition)).

`newSession` is the one verb that is never unsupported: every agent can
start a session. Omitted, it names the lifecycle@1 interactive launch.
Present, it is authoritative for the new-session invocation, and a
present `[]` still means the launch argv alone. Omitting it does not
count as declaring a verb, so `newSession` alone (even `[]`) is a valid
declaration and an empty `config` is not.

### Agreement with lifecycle@1

[lifecycle@1](lifecycle@1.md)'s `interactive` field is the argv tail for
the engine's TTY launch mode, and `newSession` names the same invocation.

A Kit declaring both **MUST** give them the same argv; validation rejects <!-- tck: agent-interactive-sessions@1/new-session-matches-lifecycle-interactive -->
a mismatch, so the default (omitted `newSession`) and the explicit
spelling can never disagree. A Kit declaring only one is not in conflict:
an omitted `newSession` takes the lifecycle tail, and a stated one
governs the new-session invocation on its own. An empty lifecycle `interactive`
is indistinguishable from an omitted one, so it never conflicts.

### Placeholders

`{{.Prompt}}` and `{{.SessionID}}` are substituted by the host before
execution, and may ride inside a larger token (`--prompt={{.Prompt}}`).
They are the same placeholders, with the same rules, as in
[agent-sessions@1](agent-sessions@1.md#placeholders): the placeholder is
the verb's whole point, so a verb that never receives the prompt or the
session id would run the agent with the caller's input silently
discarded, and the references are validation requirements.

## Runtime behavior

A conforming runtime (or host):

- **MUST** build each interactive invocation as the workload's launch <!-- tck: agent-interactive-sessions@1/interactive-from-launch-argv -->
  argv (image `Entrypoint` + `Cmd`) plus the verb's tail, with
  placeholders substituted — the same way user-supplied args append. Verb
  tails never replace the launch command.
- **MUST** start a new interactive session, when `newSession` is absent, <!-- tck: agent-interactive-sessions@1/absent-new-session-is-lifecycle-launch -->
  exactly as the [lifecycle@1](lifecycle@1.md) interactive launch mode
  does.
- **MUST** launch every interactive verb with a terminal attached, the <!-- tck: agent-interactive-sessions@1/terminal-attached -->
  same way as the lifecycle@1 interactive launch mode.
- **MUST** substitute the raw caller values (no shell re-quoting into the <!-- tck: agent-interactive-sessions@1/raw-value-substitution -->
  argv elements — the tail is exec argv, not a shell string).
- **MUST** run `list` as its own complete command and parse stdout as ids, <!-- tck: agent-interactive-sessions@1/list-parses-stdout -->
  one per line, most recent first; ids feed `resume` verbatim.
- **MUST** treat an absent verb other than `newSession` as "operation <!-- tck: agent-interactive-sessions@1/absent-verb-unsupported -->
  unsupported" and surface that, rather than improvising flags.
- **MUST NOT** treat the declaration as a permission: it grants nothing; <!-- tck: agent-interactive-sessions@1/declaration-grants-nothing -->
  it teaches the host how to drive what the workload already runs.

## Composition

The workload Kit's declaration governs. A mixin declaring
agent-interactive-sessions has nothing to drive; composition keeps the
workload's entry, as the original declaration rather than a re-rendering
of it, so an empty tail survives composition as the verb it is.
