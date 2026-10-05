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

const sessions = [];
let skipped = 0;

for (const s of await listSessions()) {
  if (background.has(s.sessionId)) continue;
  if (cwdFilter && s.cwd !== cwdFilter) continue;
  if (!s.cwd) {
    skipped++;
    continue;
  }

  sessions.push({
    sessionId: s.sessionId,
    cwd: s.cwd,
    title: s.customTitle ?? s.summary,
    updatedAt: new Date(s.lastModified).toISOString(),
    _meta: {
      kind: "interactive",
      firstPrompt: s.firstPrompt,
      gitBranch: s.gitBranch,
      tag: s.tag,
      fileSize: s.fileSize,
      createdAt: s.createdAt ? new Date(s.createdAt).toISOString() : undefined,
    },
  });
}

if (skipped) console.error(`skipped ${skipped} session(s) with no cwd`);
console.log(JSON.stringify(sessions, null, 2));
