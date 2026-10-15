#!/usr/bin/env node

import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { accessSync, constants, statSync } from "node:fs";
import { homedir } from "node:os";
import { delimiter, join } from "node:path";
import { createInterface } from "node:readline";

const adapter =
  "/opt/claude-agent-acp/lib/node_modules/@agentclientprotocol/claude-agent-acp/dist/index.js";

function isExecutable(path) {
  try {
    accessSync(path, constants.X_OK);
    return statSync(path).isFile();
  } catch {
    return false;
  }
}

// The claude this composition installed, wherever its kit put it:
// claude-mixin installs /usr/local/bin/claude, the claude workload kit
// $HOME/.local/bin/claude. An explicit CLAUDE_CODE_EXECUTABLE wins when
// it exists; otherwise PATH order, so the adapter drives the same binary
// an interactive `claude` runs. The SDK's vendored binary is omitted at
// build time, so without this the adapter would find no agent at all.
function resolveClaude() {
  const fromPath = (process.env.PATH ?? "")
    .split(delimiter)
    .filter(Boolean)
    .map((dir) => join(dir, "claude"));
  const candidates = [
    process.env.CLAUDE_CODE_EXECUTABLE,
    ...fromPath,
    "/usr/local/bin/claude",
    join(homedir(), ".local/bin/claude"),
  ].filter(Boolean);
  return { found: candidates.find(isExecutable), candidates };
}

const claude = resolveClaude();
if (!claude.found) {
  console.error(
    `claude-agent-acp: no claude binary found; looked for ${[...new Set(claude.candidates)].join(", ")}`,
  );
  process.exit(127);
}
const env = { ...process.env, CLAUDE_CODE_EXECUTABLE: claude.found };

const child = spawn(process.execPath, [adapter, ...process.argv.slice(2)], {
  env,
  stdio: ["pipe", "pipe", "inherit"],
});

const exited = new Promise((resolve) => {
  child.once("error", (error) => {
    console.error(`claude-agent-acp: ${error.message}`);
    resolve(1);
  });
  child.once("exit", (code, signal) => resolve(code ?? (signal ? 1 : 0)));
});

for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.once(signal, () => child.kill(signal));
}

function cliIsAuthenticated() {
  const probe = spawnSync(claude.found, ["auth", "status", "--json"], {
    encoding: "utf8",
    env,
    timeout: 5_000,
  });

  if (probe.status !== 0) return false;
  return JSON.parse(probe.stdout).loggedIn === true;
}

function isFalseLoggedOutNotification(line) {
  try {
    const message = JSON.parse(line);
    return (
      message.method === "_auth/status_update" &&
      message.params?.authStatus?.kind === "none" &&
      cliIsAuthenticated()
    );
  } catch {
    return false;
  }
}

function translateClientMessage(line) {
  try {
    const message = JSON.parse(line);
    if (message.method !== "session/set_model") return line;

    message.method = "session/set_config_option";
    message.params = {
      sessionId: message.params?.sessionId,
      configId: "model",
      value: message.params?.modelId,
    };
    return JSON.stringify(message);
  } catch {
    return line;
  }
}

const inputLines = createInterface({
  input: process.stdin,
  crlfDelay: Infinity,
});

async function forwardInput() {
  try {
    for await (const line of inputLines) {
      const translated = translateClientMessage(line);
      if (!child.stdin.write(`${translated}\n`))
        await once(child.stdin, "drain");
    }
  } catch (error) {
    if (child.exitCode === null && child.signalCode === null) throw error;
  } finally {
    if (!child.stdin.destroyed) child.stdin.end();
  }
}

async function forwardOutput() {
  const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });
  for await (const line of lines) {
    if (isFalseLoggedOutNotification(line)) continue;
    if (!process.stdout.write(`${line}\n`))
      await once(process.stdout, "drain");
  }
}

const input = forwardInput();
const output = forwardOutput();
const exitCode = await exited;
inputLines.close();
process.stdin.pause();
await Promise.all([input, output]);
process.exitCode = exitCode;
