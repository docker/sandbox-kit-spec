# Mem0 agent context

A declaration-only mixin that teaches agents when and how to use the
Mem0 MCP tools. It adds no binaries and does not register the MCP server;
attach the `memory` server separately.

The `scope` argument supplies the default `user_id` for cross-project
context. In a Git repository, the guidance tells agents to derive a
stable project-specific `user_id` from the canonical origin URL. This
keeps repository decisions shared across clones and worktrees without
mixing them into personal preferences.

## Use with `sbx`

```sh
sbx mcp add memory --url http://127.0.0.1:8000/mcp
sbx run claude . \
  --kit ./examples/mem0-context \
  --kit-arg mem0-context.scope=runtime-kits \
  --static-mcp memory
```

## Use with `sbxenv.yaml`

```yaml
kits:
  - source: ./examples/mem0-context
    args:
      scope: runtime-kits

mcp:
  servers:
    - name: memory
      url: http://127.0.0.1:8000/mcp
```

The agent workload supplies its own context destination. This mixin
contributes only the Mem0 guidance body, so it can compose with any agent
Kit declaring `agent-context@1`.
