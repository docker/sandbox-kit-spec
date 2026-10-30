"""MCP tools backed by the official self-hosted Mem0 REST API."""

import os
from typing import Annotated
from uuid import UUID

import httpx
from mcp.server.mcpserver import MCPServer
from mcp.server.mcpserver.exceptions import ToolError
from mcp.server.transport_security import TransportSecuritySettings
from pydantic import Field, StringConstraints

Text = Annotated[str, StringConstraints(strip_whitespace=True, min_length=1)]
Limit = Annotated[int, Field(ge=1, le=1000)]
ENTITY_FIELDS = ("user_id", "agent_id", "run_id")


async def request(method, path, **kwargs):
    headers = {"X-API-Key": os.environ["ADMIN_API_KEY"]}
    # Extraction can include several provider calls and retries. httpx's
    # five-second default reports a false tool failure while the REST
    # handler continues and may commit the memory, prompting duplicates
    # when an agent retries.
    timeout = httpx.Timeout(180.0, connect=10.0)
    try:
        async with httpx.AsyncClient(
            base_url="http://127.0.0.1:8000", timeout=timeout
        ) as client:
            response = await client.request(method, path, headers=headers, **kwargs)
    except httpx.TimeoutException as exc:
        raise ToolError("Mem0 did not finish within three minutes") from exc
    if response.is_error:
        try:
            detail = response.json().get("detail")
        except ValueError:
            detail = None
        raise ToolError(detail or f"Mem0 API returned HTTP {response.status_code}")
    return response.json()


def scoped_filters(filters):
    filters = dict(filters or {})
    if not any(filters.get(field) for field in ENTITY_FIELDS):
        filters["user_id"] = os.environ.get("MEM0_DEFAULT_USER_ID", "mem0-mcp")
    return filters


def simple_scope(filters):
    filters = scoped_filters(filters)
    unsupported = set(filters) - set(ENTITY_FIELDS)
    if unsupported:
        raise ToolError(
            "Listing supports only user_id, agent_id, and run_id filters; "
            "use search_memories for structured filters"
        )
    if not all(isinstance(value, str) and value for value in filters.values()):
        raise ToolError("Entity filters must be nonempty strings")
    return filters


def create_mcp_app():
    server = MCPServer(
        "mem0",
        subscriptions=False,
        instructions=(
            "Persistent memory backed by the official self-hosted Mem0 server. "
            "Use a consistent entity scope across agents and confirm destructive operations."
        ),
    )

    @server.tool()
    async def add_memory(
        text: Text | None = None,
        messages: list[dict[str, str]] | None = None,
        user_id: Text | None = None,
        agent_id: Text | None = None,
        run_id: Text | None = None,
        metadata: dict | None = None,
        infer: bool = True,
    ) -> dict:
        """Save text or role/content messages under an entity scope."""
        if not messages and not text:
            raise ToolError("Provide text or messages to store")
        if messages is not None and (
            not messages
            or any(
                message.get("role") not in ("user", "assistant", "system")
                or not message.get("content", "").strip()
                for message in messages
            )
        ):
            raise ToolError("messages must contain nonempty role/content messages")
        if user_id is None and agent_id is None and run_id is None:
            user_id = os.environ.get("MEM0_DEFAULT_USER_ID", "mem0-mcp")
        return await request(
            "POST",
            "/memories",
            json={
                "messages": messages or [{"role": "user", "content": text}],
                "user_id": user_id,
                "agent_id": agent_id,
                "run_id": run_id,
                "metadata": metadata or {},
                "infer": infer,
            },
        )

    @server.tool()
    async def search_memories(
        query: Text, filters: dict | None = None, limit: Limit = 10
    ) -> dict:
        """Semantically search memories with Mem0 structured filters."""
        return await request(
            "POST",
            "/search",
            json={"query": query, "filters": scoped_filters(filters), "top_k": limit},
        )

    @server.tool()
    async def get_memories(
        filters: dict | None = None, limit: Limit = 100
    ) -> dict:
        """List memories for user_id, agent_id, or run_id filters."""
        return await request(
            "GET", "/memories", params={**simple_scope(filters), "top_k": limit}
        )

    @server.tool()
    async def get_memory(memory_id: UUID) -> dict:
        """Retrieve one memory by its exact ID."""
        return await request("GET", f"/memories/{memory_id}")

    @server.tool()
    async def update_memory(
        memory_id: UUID, text: Text | None = None, metadata: dict | None = None
    ) -> dict:
        """Update the text and/or metadata of one memory."""
        if text is None and metadata is None:
            raise ToolError("Provide text or metadata to update")
        return await request(
            "PUT",
            f"/memories/{memory_id}",
            json={"text": text, "metadata": metadata},
        )

    @server.tool()
    async def delete_memory(memory_id: UUID) -> dict:
        """Delete one memory after confirming its exact ID."""
        return await request("DELETE", f"/memories/{memory_id}")

    @server.tool()
    async def delete_all_memories(
        user_id: Text | None = None,
        agent_id: Text | None = None,
        run_id: Text | None = None,
    ) -> dict:
        """Delete memories matching all supplied entity IDs."""
        scope = {
            key: value
            for key, value in zip(
                ENTITY_FIELDS, (user_id, agent_id, run_id), strict=True
            )
            if value is not None
        }
        return await request("DELETE", "/memories", params=simple_scope(scope))

    @server.tool()
    async def delete_entities(
        user_id: Text | None = None,
        agent_id: Text | None = None,
        run_id: Text | None = None,
    ) -> dict:
        """Delete explicitly named entities and their memories."""
        scope = {
            key: value
            for key, value in zip(
                ENTITY_FIELDS, (user_id, agent_id, run_id), strict=True
            )
            if value is not None
        }
        if not scope:
            raise ToolError("Provide user_id, agent_id, or run_id")
        for field, value in scope.items():
            # Query encoding preserves delimiters in entity IDs. Each request
            # deletes one entity, matching the upstream entity endpoint.
            await request("DELETE", "/memories", params={field: value})
        return {"message": "Entities deleted"}

    @server.tool()
    async def list_entities() -> list[dict]:
        """List user, agent, and run entities known to the server."""
        return await request("GET", "/entities")

    return server.streamable_http_app(
        json_response=True,
        stateless_http=True,
        transport_security=TransportSecuritySettings(
            allowed_hosts=[
                host + suffix
                for host in ("localhost", "127.0.0.1", "[::1]", "host.docker.internal")
                for suffix in ("", ":*")
            ],
            allowed_origins=[
                "http://" + host + suffix
                for host in ("localhost", "127.0.0.1", "[::1]", "host.docker.internal")
                for suffix in ("", ":*")
            ],
        ),
    )
