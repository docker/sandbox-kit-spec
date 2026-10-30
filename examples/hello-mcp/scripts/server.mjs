#!/usr/bin/env node
// A dependency-free MCP server over stdio: one JSON-RPC message per line
// on stdin and stdout. Stdout carries protocol messages only, so every
// diagnostic goes to stderr, and stdin EOF ends the process.
import { createInterface } from "node:readline";

const SERVER = { name: "hello-mcp", version: "1.0.0" };
// Newest first: answer with the client's version when it is one of these,
// otherwise with the newest, which is what the MCP lifecycle prescribes.
const PROTOCOL_VERSIONS = ["2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"];
const greetingFlag = process.argv.slice(2).find((arg) => arg.startsWith("--greeting="));
const greeting = greetingFlag?.slice("--greeting=".length) || "hello";

const tools = [
  {
    name: "greet",
    description: "Greets someone by name",
    inputSchema: {
      type: "object",
      properties: { name: { type: "string", description: "Who to greet" } },
      required: ["name"],
      additionalProperties: false,
    },
  },
];

function send(message) {
  process.stdout.write(JSON.stringify({ jsonrpc: "2.0", ...message }) + "\n");
}

function handle(request) {
  switch (request.method) {
    case "initialize": {
      const asked = request.params?.protocolVersion;
      return {
        protocolVersion: PROTOCOL_VERSIONS.includes(asked) ? asked : PROTOCOL_VERSIONS[0],
        capabilities: { tools: {} },
        serverInfo: SERVER,
      };
    }
    case "ping":
      return {};
    case "tools/list":
      return { tools };
    case "tools/call": {
      const { name, arguments: args } = request.params ?? {};
      if (name !== "greet") {
        throw { code: -32602, message: `unknown tool: ${name}` };
      }
      if (typeof args?.name !== "string" || args.name === "") {
        return { content: [{ type: "text", text: "name must be a non-empty string" }], isError: true };
      }
      return { content: [{ type: "text", text: `${greeting} ${args.name}` }] };
    }
    default:
      throw { code: -32601, message: `method not found: ${request.method}` };
  }
}

const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
lines.on("line", (line) => {
  if (line.trim() === "") return;
  let request;
  try {
    request = JSON.parse(line);
  } catch {
    send({ id: null, error: { code: -32700, message: "parse error" } });
    return;
  }
  // Notifications (no id) get no response; initialized is the only one
  // this server expects, and it needs no action.
  if (request.id === undefined) return;
  try {
    send({ id: request.id, result: handle(request) });
  } catch (error) {
    const { code = -32603, message = String(error) } = error ?? {};
    send({ id: request.id, error: { code, message } });
  }
});
lines.on("close", () => process.exit(0));
process.on("SIGTERM", () => process.exit(0));
console.error(`${SERVER.name} ${SERVER.version} ready on stdio`);
