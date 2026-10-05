#!/usr/bin/env node
// Enumerate interactive Claude Code sessions for agent-sessions@1 list.
// Background agents (claude agents --json --all) are excluded; those are
// a different control surface. Optional argv[2] is an absolute cwd filter
// (exact match), matching ACP's cwd filter.
import { execFileSync } from "node:child_process";
import { listSessions } from "@anthropic-ai/claude-agent-sdk";

const cwdFilter = process.argv[2];

const rows = JSON.parse(
  execFileSync("claude", ["agents", "--json", "--all"], {
    encoding: "utf8",
    maxBuffer: 64 * 1024 * 1024,
  })
);
const background = new Set(
  rows.filter((r) => r.kind === "background" && r.sessionId).map((r) => r.sessionId)
);

// Sort explicitly: the v1 contract orders IDs by most recent session,
// regardless of the SDK's traversal order. A missing cwd still has a
// resumable ID; it matters only when the caller requests a cwd filter.
const sessions = (await listSessions())
  .filter((s) => s.sessionId && !background.has(s.sessionId))
  .filter((s) => !cwdFilter || s.cwd === cwdFilter)
  .sort((a, b) => b.lastModified - a.lastModified);

for (const s of sessions) process.stdout.write(`${s.sessionId}\n`);
