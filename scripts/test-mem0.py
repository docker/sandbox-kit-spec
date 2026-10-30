"""Offline stack and regression checks, run inside the built Mem0 image."""

import os
from pathlib import Path
import subprocess
import tempfile
import time
from uuid import uuid4

import httpx
import psycopg
from psycopg.types.json import Jsonb


def call_tool(name, arguments=None, *, error=False):
    response = httpx.post(
        "http://127.0.0.1:8000/mcp",
        headers={
            "Accept": "application/json, text/event-stream",
            "MCP-Protocol-Version": "2025-03-26",
            "Host": "localhost:8000",
        },
        json={
            "jsonrpc": "2.0", "id": 1, "method": "tools/call",
            "params": {"name": name, "arguments": arguments or {}},
        },
        timeout=30,
    )
    response.raise_for_status()
    result = response.json()["result"]
    assert result.get("isError", False) == error, result
    return result


def wait_ready(stack):
    for _ in range(90):
        assert stack.poll() is None, "Mem0 exited before becoming ready"
        try:
            for path in (
                "http://127.0.0.1:8000/openapi.json",
                "http://127.0.0.1:3000/api/health",
                "http://127.0.0.1:3000/setup",
            ):
                httpx.get(path, timeout=2).raise_for_status()
            call_tool("get_memories")
            return
        except (httpx.HTTPError, AssertionError, KeyError):
            time.sleep(1)
    raise AssertionError("Mem0 did not become ready within 90 seconds")


def connection(dbname="postgres"):
    return psycopg.connect(host="127.0.0.1", user="agent", dbname=dbname)


def seed(**scope):
    # Seed vectors directly so deletion tests exercise the real store and
    # HTTP transports without sending test data to an embedding provider.
    memory_id = uuid4()
    with connection() as db:
        db.execute(
            "INSERT INTO memories (id, payload) VALUES (%s, %s)",
            (memory_id, Jsonb({"data": "offline regression", **scope})),
        )
    return str(memory_id)


def present(memory_id):
    with connection() as db:
        return db.execute(
            "SELECT EXISTS (SELECT FROM memories WHERE id = %s)", (memory_id,)
        ).fetchone()[0]


def check_deletion():
    for field in ("user_id", "agent_id", "run_id"):
        for entity_id in ("team?old", "team/old", "team#old", "team%2Fold", "team&old"):
            neighbor = seed(**{field: "team"})
            target = seed(**{field: entity_id})
            call_tool("delete_entities", {field: entity_id})
            assert not present(target), (field, entity_id)
            assert present(neighbor), (field, entity_id, "deleted neighboring entity")

    both = seed(user_id="bulk", agent_id="worker")
    user_only = seed(user_id="bulk", agent_id="other")
    agent_only = seed(user_id="other", agent_id="worker")
    call_tool("delete_all_memories", {"user_id": "bulk", "agent_id": "worker"})
    assert not present(both)
    assert present(user_only) and present(agent_only), "bulk deletion lost its intersection"
    call_tool("delete_entities", {"user_id": "bulk", "agent_id": "worker"})
    assert not present(user_only) and not present(agent_only)
    call_tool("delete_entities", error=True)

    target = seed(user_id="single")
    for tool in ("get_memory", "update_memory", "delete_memory"):
        args = {"memory_id": target + "?wrong"}
        if tool == "update_memory":
            args["text"] = "must not be saved"
        call_tool(tool, args, error=True)
        assert present(target), tool
    call_tool("get_memory", {"memory_id": target})
    call_tool("delete_memory", {"memory_id": target})
    assert not present(target)


def main():
    subprocess.run(["git", "--version"], check=True)
    # Quotes and SQL punctuation catch both unsafe identifier quoting and
    # the former hard-coded mem0_app database name.
    os.environ["APP_DB_NAME"] = "mem0 custom '\";--"
    with tempfile.TemporaryFile(mode="w+") as log:
        retained = None
        secret = None
        try:
            for boot in range(2):
                stack = subprocess.Popen(["/opt/mem0/start.sh"], stdout=log, stderr=log)
                try:
                    wait_ready(stack)
                    with connection(os.environ["APP_DB_NAME"]) as db:
                        assert db.execute("SELECT to_regclass('users')").fetchone()[0]
                    if boot == 0:
                        contender_data = "/var/lib/mem0/contender-postgres"
                        contender = subprocess.run(
                            ["/opt/mem0/start.sh"], timeout=10,
                            env={**os.environ, "PGDATA": contender_data},
                            capture_output=True, text=True,
                        )
                        assert contender.returncode == 0, contender.stderr
                        assert "already running" in contender.stderr
                        assert not Path(contender_data).exists(), "contender reached initialization"
                        check_deletion()
                        retained = seed(user_id="retained")
                        secret = Path("/var/lib/mem0/jwt-secret").read_text()
                    else:
                        assert present(retained), "restart lost memory data"
                        assert Path("/var/lib/mem0/jwt-secret").read_text() == secret
                finally:
                    stack.terminate()
                    stack.wait(timeout=30)
        except BaseException:
            log.seek(0)
            print(log.read())
            raise
    print("Mem0 stack, scoped deletion, shared lock, custom database and restart checks passed")


if __name__ == "__main__":
    main()
