#!/bin/sh
# sbx-agent-hook — forwards agent lifecycle and session information to sbx Desktop.
#
# The desktop app registers an MCP server per sandbox on the sandbox's MCP
# gateway (the same gateway the agent already talks to). This script turns
# harness payloads into `tools/call`s on that server so the app can raise a
# macOS notification (`sbx_desktop_notify`) and render a status line for the
# session (`sbx_desktop_session`: model, effort, context window, tokens,
# cost, lines changed, git branch). When the app is not installed, not
# running, or has not loaded its server into this sandbox, the gateway
# answers with an error and nothing else happens.
#
#   sbx-agent-hook claude          Claude Code Stop / StopFailure / Notification
#                                  hook: event JSON on stdin -> notify
#   sbx-agent-hook claude-status   Claude Code statusLine command: session JSON
#                                  on stdin -> session info (prints nothing, so
#                                  no status line is drawn in the terminal)
#   sbx-agent-hook codex           Codex `notify`: event JSON as the last
#                                  argument -> notify + session info read from
#                                  the thread's rollout file
#
# Fail-safe by construction: every path exits 0, nothing is ever written to
# stdout or stderr (Claude Code treats empty stdout + exit 0 as a clean
# no-op, Codex ignores notify output), each request is bounded by a short
# curl timeout, and missing tooling (curl, jq) or a missing gateway just
# ends the script. The agent is never blocked or shown an error.

mode="${1:-}"
input=""
case "$mode" in
  claude|claude-status) input=$(cat 2>/dev/null) ;;
  codex)
    shift
    for arg; do input="$arg"; done
    ;;
  *) exit 0 ;;
esac
[ -n "$input" ] || exit 0

url="${MCP_GATEWAY_URL:-}"
auth="Authorization: Bearer ${MCP_SENTINEL_TOKEN_NAME:-proxy-managed}"
ctype='Content-Type: application/json'
accept='Accept: application/json, text/event-stream'
state_dir="${TMPDIR:-/tmp}/sbx-agent-hook"

# --- MCP session over Streamable HTTP ---------------------------------------
sid=""
hdr=""
mcp_open() {
  hdr=$(mktemp 2>/dev/null) || return 1
  init='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"sbx-agent-hook","version":"1.1"}}}'
  if ! curl -s -m 3 -o /dev/null -D "$hdr" -H "$auth" -H "$ctype" -H "$accept" --data "$init" "$url"; then
    rm -f "$hdr"
    return 1
  fi
  sid=$(tr -d '\r' < "$hdr" | awk 'tolower($1) == "mcp-session-id:" { print $2; exit }')
  rm -f "$hdr"
  [ -n "$sid" ] || return 1
  curl -s -m 3 -o /dev/null -H "$auth" -H "$ctype" -H "$accept" -H "Mcp-Session-Id: $sid" \
    --data '{"jsonrpc":"2.0","method":"notifications/initialized"}' "$url"
  return 0
}
rpc_id=1
mcp_call() { # tool-name arguments-json
  rpc_id=$((rpc_id + 1))
  curl -s -m 5 -o /dev/null -H "$auth" -H "$ctype" -H "$accept" -H "Mcp-Session-Id: $sid" \
    --data "{\"jsonrpc\":\"2.0\",\"id\":$rpc_id,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2}}" "$url"
}
mcp_close() {
  [ -n "$sid" ] && curl -s -m 2 -o /dev/null -X DELETE -H "$auth" -H "Mcp-Session-Id: $sid" "$url"
}

# --- Payload builders --------------------------------------------------------
# Lifecycle event (Claude hook JSON or Codex notify JSON) -> notify arguments.
notify_args() { # agent
  printf '%s' "$input" | jq -c --arg agent "$1" '
    def str: if . == null then null else tostring end;
    {
      agent: $agent,
      event: ((.hook_event_name // .type // "unknown") | tostring | ascii_downcase),
      session_id: ((.session_id // ."thread-id") | str),
      cwd: (.cwd | str),
      message: (((.last_assistant_message // ."last-assistant-message" // .message // "") | tostring) | .[0:600]),
      title: (.title | str),
      notification_type: (.notification_type | str),
      stop_hook_active: ((.stop_hook_active // false) == true)
    }'
}

# Live git facts for a directory, as jq --arg values (empty when not a repo).
git_branch=""; git_dirty="false"; git_commit=""
read_git() { # dir
  [ -n "$1" ] && [ -d "$1" ] || return 0
  if git_branch=$(git -C "$1" rev-parse --abbrev-ref HEAD 2>/dev/null); then
    git_commit=$(git -C "$1" rev-parse --short HEAD 2>/dev/null)
    [ -z "$(git -C "$1" status --porcelain 2>/dev/null | head -1)" ] || git_dirty="true"
  else
    git_branch=""
  fi
}

# Attaches the git facts to a session-info object on stdin.
with_git() {
  jq -c --arg b "$git_branch" --arg c "$git_commit" --argjson d "$git_dirty" '
    . + { git_branch: (if $b == "" then .git_branch else $b end),
          git_commit: (if $c == "" then .git_commit else $c end),
          git_dirty: (if $b == "" then .git_dirty else $d end) }'
}

# Claude Code statusLine JSON -> session arguments.
claude_session_args() {
  effort=""
  [ -r "$HOME/.claude/settings.json" ] && effort=$(jq -r '.effortLevel // .env.CLAUDE_CODE_EFFORT_LEVEL // empty' "$HOME/.claude/settings.json" 2>/dev/null)
  [ -n "$effort" ] || effort="${CLAUDE_CODE_EFFORT_LEVEL:-}"
  printf '%s' "$input" | jq -c --arg effort "$effort" '
    def num: if . == null then null else tonumber end;
    {
      agent: "claude",
      session_id: (.session_id // null),
      cwd: (.workspace.current_dir // .cwd // null),
      model_id: (.model.id // null),
      model_name: (.model.display_name // .model.id // null),
      effort: (if $effort == "" then null else $effort end),
      version: (.version // null),
      context_used_tokens: (.context_window.total_input_tokens | num),
      context_window: (.context_window.context_window_size | num),
      context_percent: (.context_window.used_percentage | num),
      input_tokens: (.context_window.total_input_tokens | num),
      output_tokens: (.context_window.total_output_tokens | num),
      cost_usd: (.cost.total_cost_usd | num),
      duration_ms: (.cost.total_duration_ms | num),
      lines_added: (.cost.total_lines_added | num),
      lines_removed: (.cost.total_lines_removed | num),
      git_branch: null, git_commit: null, git_dirty: null
    }'
}

# Codex rollout file for a thread -> session arguments.
codex_session_args() { # thread-id
  codex_store="${CODEX_HOME:-$HOME/.codex}"
  file=$(find "$codex_store/sessions" -name "rollout-*-$1.jsonl" -type f 2>/dev/null | head -1)
  [ -n "$file" ] || return 1
  jq -n -c '
    [inputs] as $all
    | ($all | map(select(.type == "session_meta")) | last | .payload) as $meta
    | ($all | map(select(.type == "turn_context")) | last | .payload) as $turn
    | ($all | map(select(.type == "event_msg" and .payload.type == "token_count")) | last | .payload.info) as $tok
    | ($tok.model_context_window // null) as $win
    | ($tok.last_token_usage.total_tokens // null) as $used
    | {
        agent: "codex",
        session_id: ($meta.id // null),
        cwd: ($turn.cwd // $meta.cwd // null),
        model_id: ($turn.model // null),
        model_name: ($turn.model // null),
        effort: ($turn.effort // null),
        version: ($meta.cli_version // null),
        context_used_tokens: $used,
        context_window: $win,
        context_percent: (if ($win != null and $win > 0 and $used != null) then (($used * 100 / $win) | floor) else null end),
        input_tokens: ($tok.total_token_usage.input_tokens // null),
        output_tokens: ($tok.total_token_usage.output_tokens // null),
        cost_usd: null, duration_ms: null, lines_added: null, lines_removed: null,
        git_branch: ($meta.git.branch // null),
        git_commit: (($meta.git.commit_hash // null) | if . == null then null else .[0:7] end),
        git_dirty: null
      }' "$file"
}

# Sends session info unless it is identical to what this session sent last.
send_session() { # args-json
  [ -n "$1" ] || return 0
  key=$(printf '%s' "$1" | jq -r '.session_id // "unknown"' 2>/dev/null | tr -c 'A-Za-z0-9_.-' '_')
  mkdir -p "$state_dir" 2>/dev/null
  stamp="$state_dir/$key.session"
  sum=$(printf '%s' "$1" | cksum | cut -d' ' -f1)
  [ -r "$stamp" ] && [ "$(cat "$stamp" 2>/dev/null)" = "$sum" ] && return 0
  mcp_open || return 0
  mcp_call sbx_desktop_session "$1"
  mcp_close
  printf '%s' "$sum" > "$stamp" 2>/dev/null
}

{
  [ -n "$url" ] || exit 0
  command -v curl >/dev/null 2>&1 || exit 0
  command -v jq >/dev/null 2>&1 || exit 0

  case "$mode" in
    claude)
      args=$(notify_args claude) || exit 0
      [ -n "$args" ] || exit 0
      # A Stop that a previous Stop hook already continued is the same turn.
      case "$args" in *'"stop_hook_active":true'*) exit 0 ;; esac
      mcp_open || exit 0
      mcp_call sbx_desktop_notify "$args"
      mcp_close
      ;;
    claude-status)
      # Claude Code cancels an in-flight statusLine command when the next
      # update arrives, so the network work runs detached and the command
      # itself returns at once (and prints nothing).
      args=$(claude_session_args) || exit 0
      [ -n "$args" ] || exit 0
      dir=$(printf '%s' "$args" | jq -r '.cwd // empty')
      (
        read_git "$dir"
        send_session "$(printf '%s' "$args" | with_git)"
      ) </dev/null >/dev/null 2>&1 &
      ;;
    codex)
      args=$(notify_args codex) || exit 0
      [ -n "$args" ] || exit 0
      tid=$(printf '%s' "$input" | jq -r '."thread-id" // empty')
      session=""
      if [ -n "$tid" ]; then
        session=$(codex_session_args "$tid") || session=""
        if [ -n "$session" ]; then
          read_git "$(printf '%s' "$session" | jq -r '.cwd // empty')"
          session=$(printf '%s' "$session" | with_git)
        fi
      fi
      mcp_open || exit 0
      mcp_call sbx_desktop_notify "$args"
      [ -n "$session" ] && mcp_call sbx_desktop_session "$session"
      mcp_close
      ;;
  esac
} >/dev/null 2>&1
exit 0
