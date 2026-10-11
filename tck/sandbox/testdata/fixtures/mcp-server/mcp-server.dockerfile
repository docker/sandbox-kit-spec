# A real stdio MCP server with no tools, so a runtime that wrongly boots
# this image gets a process that answers MCP rather than one that exits
# and hides the mistake behind a failed start.
FROM dhi.io/node:24-debian13
COPY <<'EOF' /app/server.mjs
import { createInterface } from "node:readline";
const send = (m) => process.stdout.write(JSON.stringify({ jsonrpc: "2.0", ...m }) + "\n");
const lines = createInterface({ input: process.stdin });
lines.on("line", (line) => {
  let r;
  try { r = JSON.parse(line); } catch { return; }
  if (r.id === undefined) return;
  if (r.method === "initialize") {
    send({ id: r.id, result: { protocolVersion: r.params?.protocolVersion ?? "2025-06-18", capabilities: { tools: {} }, serverInfo: { name: "kit-tck", version: "1.0.0" } } });
  } else if (r.method === "tools/list") {
    send({ id: r.id, result: { tools: [] } });
  } else {
    send({ id: r.id, error: { code: -32601, message: "method not found" } });
  }
});
lines.on("close", () => process.exit(0));
EOF
ENTRYPOINT ["node", "/app/server.mjs"]
