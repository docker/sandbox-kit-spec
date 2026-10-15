# Mem0 agent context

A declaration-only mixin that teaches agents when and how to use the
Mem0 MCP tools. It adds no binaries and does not register the MCP server;
attach the `memory` server separately.

The `scope` argument becomes the `user_id` used by every memory call.
Give agents working on one project the same scope, and use different
scopes to keep unrelated project memories apart.

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
