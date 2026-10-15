# Mem0

The official self-hosted Mem0 server and dashboard, built from the pinned
Mem0 release, plus an MCP adapter for agents. The Kit runs PostgreSQL
with pgvector, the FastAPI server, and the Next.js dashboard in one
sandbox. PostgreSQL data, application accounts, configuration, API keys,
audit logs, and memory history persist in the runtime-managed host mount.

Extraction uses OpenAI (`gpt-5-mini` by default); embeddings use
`text-embedding-3-small`. Memory text is sent to OpenAI for those
operations. Anonymous Mem0 telemetry is disabled.

## Run

The Kit requests a separate `mem0-openai` service credential so it can
coexist with an `openai` secret holding agent OAuth credentials. A
ChatGPT subscription credential does not supply the API key this server
needs:

```sh
printf '%s' "${OPENAI_API_KEY:?Set OPENAI_API_KEY to your OpenAI API key}" \
  | sbx secret set mem0-openai
```

Create the sandbox, then publish the fixed loopback ports expected by the
browser bundle and CORS configuration:

```sh
sbx create --name mem0 ./examples/mem0
sbx ports mem0 --publish 127.0.0.1:8000:8000
sbx ports mem0 --publish 127.0.0.1:3000:3000
```

Open <http://127.0.0.1:3000>. A fresh installation redirects to the
one-time setup wizard. Create the first administrator there, then sign in
to use:

- Requests — API audit logs.
- Memories — browse and search stored memories.
- Entities — inspect and delete user, agent, and run scopes.
- API Keys — issue and revoke keys for programmatic access.
- Configuration — select the LLM and embedder configuration.
- Settings — manage the administrator account.

The REST API is at <http://127.0.0.1:8000>; its interactive OpenAPI
documentation is at <http://127.0.0.1:8000/docs>. Programmatic requests
use an API key created in the dashboard:

```sh
curl --fail-with-body http://127.0.0.1:8000/memories \
  -H "X-API-Key: $MEM0_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "user_id": "alice",
    "messages": [{"role": "user", "content": "I prefer tea to coffee."}]
  }'
```

## Connect agents through MCP

The MCP endpoint is <http://127.0.0.1:8000/mcp>. Its nine tools call the
official authenticated REST API over loopback, so writes from agents,
REST clients, and the dashboard share the same PostgreSQL-backed store.
The adapter uses a private, generated admin key internally; clients do
not need to supply dashboard credentials.

Register the endpoint on the host and load it into agent sandboxes:

```sh
sbx mcp add memory --url http://127.0.0.1:8000/mcp
sbx mcp load memory --sandbox <agent-sandbox-name>
```

For a new sandbox, preload the registration:

```sh
sbx run claude --name my-agent --static-mcp memory
```

Calls without an entity filter use the `mem0-mcp` user scope. Change it
with the Kit's `defaultUserId` argument, or pass `user_id`, `agent_id`,
or `run_id` to a tool. The endpoint implements `add_memory`,
`search_memories`, `get_memories`, `get_memory`, `update_memory`,
`delete_memory`, `delete_all_memories`, `delete_entities`, and
`list_entities`.

Like the REST API, MCP is reachable without transport authentication on
the published loopback port. Do not publish port 8000 on a non-loopback
host address.

The dashboard contains `127.0.0.1:8000` as its browser-facing API URL.
Runtime-assigned host ports therefore are not sufficient: publish both
fixed ports before opening the dashboard. From another sandbox, use the
host gateway and an API key, and allow the host port in that sandbox's
network policy.

The entrypoint remains a Bash login shell. A background startup hook runs
PostgreSQL, database migrations, the API, and the dashboard independently
of shell sessions. Hook output is available in
`/var/log/sbx-kit-startup.log`; PostgreSQL writes
`/var/lib/mem0/postgres.log`.

## Persistence and upgrades

The required `host-mount@1` request mounts `/var/lib/mem0`. The runtime
chooses the host directory. Its contents survive sandbox removal and are
shared by sandboxes with the same Kit identity:

| Path | Content |
|---|---|
| `postgres/` | pgvector memories, users, API keys, configuration, and audit logs |
| `history.db` | Mem0 memory history |
| `jwt-secret` | Generated dashboard session-signing secret |
| `postgres.log` | PostgreSQL startup and server log |

The prior example implementation stored vectors under `qdrant/` and its
entity registry in `service.db`. The official server cannot import those
files directly; they remain untouched after this upgrade. Export old
memories before switching when that data must be retained.

Only one Mem0 sandbox should use a given host directory at a time. The
startup hook holds `/tmp/mem0-server.lock`, and PostgreSQL also refuses a
second server against the same data directory.

PostgreSQL major-version upgrades require `pg_dump`/`pg_restore`; do not
point a newer major version at an older data directory. Stop the sandbox
before copying the host mount, and back up the whole directory together.

## Build and verify

```sh
task kit:build KIT=mem0
task test:mem0
```

The `version` argument pins the Python package, the Git release used for
the server and dashboard sources, the Kit version, and its provide.
Refresh it with `task versions:update KIT=mem0`. The `model` create
argument selects the default extraction model.

The image build checks the release source and installed Python package
versions. The Mem0 test builds the Kit, starts its complete internal
stack, waits for the API and dashboard, and calls `get_memories` through
MCP.
