# Agent context discovery destinations

The example agent workloads and mixins declare explicit
`agent-context@1` profiles so runtime guidance and the Kit index reach the
files their harnesses discover. Staged Kit bodies remain under
`/usr/share/sandbox/kit/`; the discovery profile points at those bodies.

| Harness | Example profile | Discovery constraints |
|---|---|---|
| Codex | `/home/agent/.codex/AGENTS.md` | Global instructions live in `CODEX_HOME`. Project discovery stops at the repository root. `AGENTS.override.md` takes precedence over `AGENTS.md`. |
| Claude Code | `/home/agent/.claude/CLAUDE.md` | User instructions use the Claude configuration directory. ACP enables user, project, and local setting sources. |
| Gemini CLI | `/home/agent/.gemini/GEMINI.md` | Global instructions use the agent home directory; `context.fileName` can change the filename. |
| OpenCode | `/home/agent/.config/opencode/AGENTS.md` | The default global configuration directory is separate from project discovery. `XDG_CONFIG_HOME` and `OPENCODE_CONFIG_DIR` can change it. |
| Docker Agent | `/home/agent/AGENTS.md` | The selected agent needs `add_prompt_files` enabled. Home instructions supplement the nearest project file; `DOCKER_AGENT_KIT_DIR` replaces the home source with its `prompt_files/AGENTS.md`. |
| Cursor CLI | `/home/agent/workspace/AGENTS.md` | Instructions are discovered at the project root. The example declares its image workdir; a runtime using another checkout root needs to configure that destination. |
| Devin CLI | Legacy workspace-sibling `AGENTS.md` | Its configuration directory is known, but an instruction discovery destination has not been verified. Both examples retain their legacy declarations. |

These are the example images' defaults. If a runtime changes an agent's
home, configuration directory, instruction filename, or project root, it
also needs to select a matching profile destination. Absolute directory
configuration does not automatically derive the root of an arbitrary
checkout. Existing user instructions outside runtime-managed sections
are preserved, including a Cursor project's authored `AGENTS.md`.

An agent mixin declares both directory and filename; a tool mixin
contributes only a body. An explicit agent profile takes precedence over
a generic workload's legacy filename, regardless of input order. This
also fixes the shell-based Codex and Claude ACP sets, which declare their
agent profiles explicitly even when composed from older published mixins.

Several agents can share one sandbox. Each explicit profile is its own
destination, so the Claude and Codex mixins together produce both
`/home/agent/.claude/CLAUDE.md` and `/home/agent/.codex/AGENTS.md`, each
with the runtime guidance and an index of every Kit, the other agent
included. Profiles naming the same directory and filename are one
destination. An earlier revision refused differing explicit profiles,
which broke stacking two agent mixins on a shell from the release that
introduced the directory field until this was relaxed.

The optional directory extends `agent-context@1` in place. Descriptors
that omit it retain their default placement. Older strict readers reject
new declarations, and a runtime needs to implement the destination,
precedence, and preservation duties before using the updated examples.
This repository defines and tests that contract; it does not contain the
`sbx` runtime handler.

## Sources

- [Codex instruction discovery](https://developers.openai.com/codex/guides/agents-md/).
- [Claude Code memory](https://code.claude.com/docs/en/memory) and
  [Claude ACP user-setting sources](https://github.com/zed-industries/claude-agent-acp/blob/v0.84.0/src/acp-agent.ts).
- [Codex ACP thread startup](https://github.com/zed-industries/codex-acp/blob/v2.0.1/src/CodexAcpClient.ts).
- [Gemini instruction discovery](https://github.com/google-gemini/gemini-cli/blob/c6bccb7ecbf6d8368d995455dd725ed34466faad/docs/cli/gemini-md.md).
- [OpenCode global and project instruction discovery](https://github.com/anomalyco/opencode/blob/v1.18.33/packages/opencode/src/session/instruction.ts).
- [Docker Agent prompt-file lookup](https://github.com/docker/docker-agent/blob/967131349e0ae618bc513815c8560b62e0c0c977/pkg/promptfiles/lookup.go).
- [Cursor CLI instruction files](https://cursor.com/docs/cli/using).
- [Devin CLI public repository](https://github.com/CognitionAI/devin-cli).

The Codex and OpenCode findings match the versions pinned by the examples
(0.159.2 and 1.18.33). Claude ACP and Codex ACP findings match their pinned
adapters (0.84.0 and 2.0.1). Gemini, Docker Agent, and Cursor findings come
from public docs or the source snapshots above; their example templates
do not pin a CLI version. Devin's official docs were unavailable during
verification, so no global destination is inferred from its config path.
