package examples_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
	"github.com/stretchr/testify/require"
)

func TestCodexDesktopHookSendsNotificationAndRolloutStatus(t *testing.T) {
	requireExampleTool(t, "jq")
	bin := t.TempDir()
	writeExampleFixture(t, filepath.Join(bin, "curl"), `#!/bin/sh
payload=""; headers=""; method=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --data) shift; payload="$1" ;;
    -D) shift; headers="$1" ;;
    -X) shift; method="$1" ;;
  esac
  shift
done
if [ -n "$headers" ]; then
  printf 'Mcp-Session-Id: test\r\n' > "$headers"
fi
case "$payload" in
  *'"method":"tools/call"'*)
    printf '%s\n' "$payload" >> "$MOCK_CALLS"
    printf '%s' "$payload" | jq -c '{jsonrpc:"2.0",id:.id,result:{content:[]}}'
    ;;
esac
[ "$method" != DELETE ] || touch "$MOCK_CLOSED"
`, 0o755)
	for _, kit := range []string{"codex", "codex-mixin"} {
		for _, withRollout := range []bool{true, false} {
			name := "with rollout"
			if !withRollout {
				name = "missing rollout"
			}
			t.Run(kit+"/"+name, func(t *testing.T) {
				descriptor, err := os.ReadFile(filepath.Join(kit, kit+".yaml"))
				require.NoError(t, err)
				require.Contains(t, string(descriptor), `notify = ["/usr/local/bin/sbx-codex-hook", "codex"]`)
				tmp := t.TempDir()
				callsPath := filepath.Join(tmp, "calls")
				closed := filepath.Join(tmp, "closed")
				codexStore := filepath.Join(tmp, "custom-codex-home")
				if withRollout {
					writeExampleFixture(t, filepath.Join(codexStore, "sessions", "2026", "10", "05",
						"rollout-2026-10-05T12-00-00-thread-test.jsonl"), `{"type":"session_meta","payload":{"id":"thread-test","cwd":"/rollout/workspace","cli_version":"0.160.0","git":{"branch":"rollout-branch","commit_hash":"1234567890abcdef"}}}
{"type":"turn_context","payload":{"model":"older-model","effort":"low"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /rollout/workspace\n\n<INSTRUCTIONS>\nbe nice\n</INSTRUCTIONS>"}]}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/rollout/workspace</cwd>\n</environment_context>"}]}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"  \nUpdate the kit versions\n\nclaude and codex are stale."}]}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"On it."}]}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Also bump the mixins"}]}}
{"type":"event_msg","payload":{"type":"token_count","info":{"model_context_window":1000,"last_token_usage":{"total_tokens":10},"total_token_usage":{"input_tokens":20,"output_tokens":3}}}}
{"type":"turn_context","payload":{"cwd":"/not-a-real-hook-test-workspace","model":"current-model","effort":"high"}}
{"type":"event_msg","payload":{"type":"token_count","info":{"model_context_window":1000,"last_token_usage":{"total_tokens":120},"total_token_usage":{"input_tokens":333,"output_tokens":44}}}}
`, 0o644)
				}
				event := `{"type":"agent-turn-complete","thread-id":"thread-test","cwd":"/event/workspace","last-assistant-message":"Finished the task"}`
				stdout, stderr, err := runExampleCommand(t, []string{
					"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
					"HOME=" + tmp,
					"CODEX_HOME=" + codexStore,
					"TMPDIR=" + tmp,
					"MCP_GATEWAY_URL=http://test.invalid/mcp",
					"MOCK_CALLS=" + callsPath,
					"MOCK_CLOSED=" + closed,
				}, `{"thread-id":"wrong-stdin-thread"}`, "sh", filepath.Join(kit, "scripts", "sbx-agent-hook.sh"), "codex", event)
				require.NoError(t, err, stderr)
				require.Empty(t, stdout)
				require.Empty(t, stderr)
				require.FileExists(t, closed, "the successful Codex path must close its MCP session")
				calls, err := os.ReadFile(callsPath)
				require.NoError(t, err)
				lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
				want := []string{`{"name":"sbx_desktop_notify","arguments":{"agent":"codex","event":"agent-turn-complete","session_id":"thread-test","cwd":"/event/workspace","message":"Finished the task","title":null,"notification_type":null,"stop_hook_active":false}}`}
				if withRollout {
					want = append(want, `{"name":"sbx_desktop_session","arguments":{"agent":"codex","session_id":"thread-test","cwd":"/not-a-real-hook-test-workspace","title":"Update the kit versions","model_id":"current-model","model_name":"current-model","effort":"high","version":"0.160.0","context_used_tokens":120,"context_window":1000,"context_percent":12,"input_tokens":333,"output_tokens":44,"cost_usd":null,"duration_ms":null,"lines_added":null,"lines_removed":null,"git_branch":"rollout-branch","git_commit":"1234567","git_dirty":null}}`)
				}
				require.Len(t, lines, len(want))
				for i, expected := range want {
					var call struct {
						ID     int             `json:"id"`
						Method string          `json:"method"`
						Params json.RawMessage `json:"params"`
					}
					require.NoError(t, json.Unmarshal([]byte(lines[i]), &call))
					require.Equal(t, i+2, call.ID)
					require.Equal(t, "tools/call", call.Method)
					require.JSONEq(t, expected, string(call.Params))
				}
			})
		}
	}
}

func TestClaudeDesktopHookForwardsThePromptAsSessionTitle(t *testing.T) {
	requireExampleTool(t, "jq")
	bin := t.TempDir()
	writeExampleFixture(t, filepath.Join(bin, "curl"), `#!/bin/sh
payload=""; headers=""; method=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --data) shift; payload="$1" ;;
    -D) shift; headers="$1" ;;
    -X) shift; method="$1" ;;
  esac
  shift
done
if [ -n "$headers" ]; then
  printf 'Mcp-Session-Id: test\r\n' > "$headers"
fi
case "$payload" in
  *'"method":"tools/call"'*)
    printf '%s\n' "$payload" >> "$MOCK_CALLS"
    printf '%s' "$payload" | jq -c '{jsonrpc:"2.0",id:.id,result:{content:[]}}'
    ;;
esac
[ "$method" != DELETE ] || touch "$MOCK_CLOSED"
`, 0o755)
	for _, kit := range []string{"claude", "claude-mixin"} {
		t.Run(kit, func(t *testing.T) {
			descriptor, err := os.ReadFile(filepath.Join(kit, kit+".yaml"))
			require.NoError(t, err)
			require.Contains(t, string(descriptor), `\"UserPromptSubmit\": [ { \"hooks\": [ $HOOK ] } ]`, "the prompt hook must be registered")
			run := func(input string) []string {
				t.Helper()
				tmp := t.TempDir()
				callsPath := filepath.Join(tmp, "calls")
				closed := filepath.Join(tmp, "closed")
				stdout, stderr, err := runExampleCommand(t, []string{
					"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
					"HOME=" + tmp,
					"TMPDIR=" + tmp,
					"MCP_GATEWAY_URL=http://test.invalid/mcp",
					"MOCK_CALLS=" + callsPath,
					"MOCK_CLOSED=" + closed,
				}, input, "sh", filepath.Join(kit, "scripts", "sbx-agent-hook.sh"), "claude")
				require.NoError(t, err, stderr)
				require.Empty(t, stdout, "UserPromptSubmit stdout would be added to the prompt")
				require.Empty(t, stderr)
				calls, err := os.ReadFile(callsPath)
				if os.IsNotExist(err) {
					return nil
				}
				require.NoError(t, err)
				require.FileExists(t, closed, "a delivery must close its MCP session")
				return strings.Split(strings.TrimSpace(string(calls)), "\n")
			}
			lines := run(`{"hook_event_name":"UserPromptSubmit","session_id":"s-1","cwd":"/work/repo","prompt":"  \nFix the flaky test\n\nIt fails on CI only."}`)
			require.Len(t, lines, 1)
			var call struct {
				Params json.RawMessage `json:"params"`
			}
			require.NoError(t, json.Unmarshal([]byte(lines[0]), &call))
			require.JSONEq(t, `{"name":"sbx_desktop_session","arguments":{"agent":"claude","session_id":"s-1","cwd":"/work/repo","title":"Fix the flaky test"}}`, string(call.Params))
			require.Nil(t, run(`{"hook_event_name":"UserPromptSubmit","session_id":"s-1","cwd":"/work/repo","prompt":"   "}`), "a blank prompt is nothing to report")
		})
	}
}

func TestDesktopHookPathsComposeAcrossAgents(t *testing.T) {
	hooks := map[string]string{}
	for _, kit := range []string{"claude", "claude-mixin", "codex", "codex-mixin"} {
		recipe, err := os.ReadFile(filepath.Join(kit, kit+".dockerfile"))
		require.NoError(t, err)
		for line := range strings.SplitSeq(string(recipe), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 3 && fields[0] == "COPY" && fields[1] == "scripts/sbx-agent-hook.sh" {
				// Scratch overlays stage the file under /out before copying
				// that tree to /. Compare the installed paths, not staging paths.
				hooks[kit] = strings.TrimPrefix(fields[2], "/out")
			}
		}
		require.NotEmpty(t, hooks[kit], "the recipe must install its hook")
		descriptor, err := os.ReadFile(filepath.Join(kit, kit+".yaml"))
		require.NoError(t, err)
		require.Contains(t, string(descriptor), hooks[kit], "the config must invoke the installed hook")
	}
	for _, claude := range []string{"claude", "claude-mixin"} {
		for _, codex := range []string{"codex", "codex-mixin"} {
			t.Run(claude+" with "+codex, func(t *testing.T) {
				require.NoError(t, assemble.CheckCollisions([]assemble.Inventory{
					{Kit: claude, Files: []string{strings.TrimPrefix(hooks[claude], "/")}},
					{Kit: codex, Files: []string{strings.TrimPrefix(hooks[codex], "/")}},
				}))
			})
		}
	}
}

func TestDesktopHookRetriesFailedClaudeStatusDelivery(t *testing.T) {
	requireExampleTool(t, "jq")
	bin := t.TempDir()
	writeExampleFixture(t, filepath.Join(bin, "curl"), `#!/bin/sh
payload=""; headers=""; method=""; fail_http=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    --data) shift; payload="$1" ;;
    -D) shift; headers="$1" ;;
    -X) shift; method="$1" ;;
    --fail) fail_http=true ;;
  esac
  shift
done
if [ -n "$headers" ]; then
  printf 'Mcp-Session-Id: test\r\n' > "$headers"
fi
case "$payload" in
  *'"name":"sbx_desktop_session"'*)
    printf '%s\n' "$payload" >> "$MOCK_DELIVERIES"
    if [ "$MOCK_DELIVERY_RESULT" = http ]; then
      "$fail_http" && exit 22
      exit 0
    fi
    [ "$MOCK_DELIVERY_RESULT" = 0 ] && printf '%s' "$MOCK_RESPONSE"
    exit "$MOCK_DELIVERY_RESULT"
    ;;
esac
# Closing the session must not determine whether delivery was successful.
[ "$method" = DELETE ] && exit 7
exit 0
`, 0o755)
	for _, kit := range []string{"claude", "claude-mixin", "codex", "codex-mixin"} {
		t.Run(kit, func(t *testing.T) {
			tmp := t.TempDir()
			deliveries := filepath.Join(tmp, "deliveries")
			stamp := filepath.Join(tmp, "sbx-agent-hook", "test_.session")
			success := `{"jsonrpc":"2.0","id":2,"result":{"content":[]}}`
			response := success
			run := func(result, input string, attempts int) {
				t.Helper()
				// The real status command detaches its worker. Wait for that
				// child on exit so assertions cannot race delivery or stamping.
				stdout, stderr, err := runExampleCommand(t, []string{
					"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
					"TMPDIR=" + tmp,
					"HOME=" + tmp,
					"MCP_GATEWAY_URL=http://test.invalid/mcp",
					"MOCK_DELIVERIES=" + deliveries,
					"MOCK_DELIVERY_RESULT=" + result,
					"MOCK_RESPONSE=" + response,
				}, input, "sh", "-c", `script=$1; shift; trap 'wait' EXIT; . "$script"`,
					"hook-test", filepath.Join(kit, "scripts", "sbx-agent-hook.sh"), "claude-status")
				require.NoError(t, err, stderr)
				require.Empty(t, stdout)
				require.Empty(t, stderr)
				calls, err := os.ReadFile(deliveries)
				require.NoError(t, err)
				require.Len(t, strings.Split(strings.TrimSpace(string(calls)), "\n"), attempts)
			}
			input := `{"session_id":"test","model":{"id":"first"}}`
			for i, result := range []string{"7", "28", "http"} {
				run(result, input, i+1)
				require.NoFileExists(t, stamp, "failed delivery must remain eligible for retry")
			}
			invalid := []string{
				`{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"unknown tool"}}`,
				`{"jsonrpc":"2.0","id":2,"result":{"content":[],"isError":true}}`,
				`{"jsonrpc":"2.0","id":3,"result":{"content":[]}}`,
				`{"jsonrpc":"2.0","result":{"content":[]}}`,
				`{"jsonrpc":"2.0","id":2,"result":null}`,
				`{"jsonrpc":"2.0","id":2,"result":{"content":[],"isError":null}}`,
				`{"jsonrpc":"2.0","id":2,"result":{}}`,
				`{"jsonrpc":"2.0","id":2`,
				"",
				"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[],\"isError\":true}}\n\n",
			}
			attempts := 3
			for _, body := range invalid {
				response = body
				attempts++
				run("0", input, attempts)
				require.NoFileExists(t, stamp, "an invalid MCP acknowledgement must not be cached: %s", body)
			}
			response = success
			attempts++
			run("0", input, attempts)
			previous, err := os.ReadFile(stamp)
			require.NoError(t, err)
			require.NotEmpty(t, previous)
			run("0", input, attempts)

			changed := `{"session_id":"test","model":{"id":"second"}}`
			attempts++
			run("28", changed, attempts)
			current, err := os.ReadFile(stamp)
			require.NoError(t, err)
			require.Equal(t, previous, current, "failed updates must preserve the last delivered checksum")
			response = invalid[1]
			attempts++
			run("0", changed, attempts)
			current, err = os.ReadFile(stamp)
			require.NoError(t, err)
			require.Equal(t, previous, current, "MCP tool errors must preserve the last delivered checksum")
			// SSE may interleave notifications, use CRLF, and split JSON over
			// multiple data lines. Its matching response still acknowledges delivery.
			response = ": keepalive\r\n\r\nevent: message\r\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\r\n\r\n" +
				"event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\r\ndata: \"result\":{\"content\":[],\"isError\":false}}\r\n\r\n"
			attempts++
			run("0", changed, attempts)
			current, err = os.ReadFile(stamp)
			require.NoError(t, err)
			require.NotEqual(t, previous, current)
			run("0", changed, attempts)
			for model, body := range map[string]string{
				"sse-lf": "data: " + success + "\n\n",
				"sse-cr": "data: " + success + "\r\r",
			} {
				response = body
				changed = `{"session_id":"test","model":{"id":"` + model + `"}}`
				attempts++
				run("0", changed, attempts)
				run("0", changed, attempts)
			}
		})
	}
}

func TestDesktopHookCleansHeadersOnGatewayFailure(t *testing.T) {
	requireExampleTool(t, "jq")
	bin := t.TempDir()
	// A connection failure can still leave a partial header file. Exercise
	// the real hook with curl replaced, so no desktop server is contacted.
	writeExampleFixture(t, filepath.Join(bin, "curl"), `#!/bin/sh
while [ "$#" -gt 0 ]; do
  if [ "$1" = -D ]; then
    shift
    printf '%s\n' 'partial header' > "$1"
  fi
  shift
done
exit 7
`, 0o755)
	for _, kit := range []string{"claude", "claude-mixin", "codex", "codex-mixin"} {
		t.Run(kit, func(t *testing.T) {
			tmp := t.TempDir()
			for range 3 {
				argv := []string{"sh", filepath.Join(kit, "scripts", "sbx-agent-hook.sh")}
				input := `{"hook_event_name":"Stop","session_id":"test"}`
				if strings.HasPrefix(kit, "codex") {
					argv = append(argv, "codex", `{"type":"agent-turn-complete"}`)
				} else {
					argv = append(argv, "claude")
				}
				stdout, stderr, err := runExampleCommand(t, []string{
					"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
					"TMPDIR=" + tmp,
					"MCP_GATEWAY_URL=http://test.invalid/mcp",
				}, input, argv...)
				require.NoError(t, err, stderr)
				require.Empty(t, stdout)
				require.Empty(t, stderr)
				entries, err := os.ReadDir(tmp)
				require.NoError(t, err)
				require.Empty(t, entries, "gateway failures must not accumulate temporary header files")
			}
		})
	}
}

func TestDesktopHookSerializesConcurrentClaudeStatus(t *testing.T) {
	requireExampleTool(t, "jq")
	bin := t.TempDir()
	writeExampleFixture(t, filepath.Join(bin, "curl"), `#!/bin/sh
payload=""; headers=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --data) shift; payload="$1" ;;
    -D) shift; headers="$1" ;;
  esac
  shift
done
if [ -n "$headers" ]; then
  printf 'Mcp-Session-Id: test\r\n' > "$headers"
fi
case "$payload" in
  *'"name":"sbx_desktop_session"'*)
    if [ ! -f "$MOCK_ENTERED" ]; then
      touch "$MOCK_ENTERED"
      while [ ! -f "$MOCK_RELEASE" ]; do sleep 0.01; done
    fi
    printf '%s\n' "$payload" >> "$MOCK_DELIVERIES"
    printf '%s' "$payload" | jq -c '{jsonrpc:"2.0",id:.id,result:{content:[]}}'
    ;;
esac
`, 0o755)
	for _, kit := range []string{"claude", "claude-mixin", "codex", "codex-mixin"} {
		for _, scenario := range []string{"newest update", "duplicate update"} {
			t.Run(kit+"/"+scenario, func(t *testing.T) {
				tmp := t.TempDir()
				entered := filepath.Join(tmp, "entered")
				release := filepath.Join(tmp, "release")
				deliveries := filepath.Join(tmp, "deliveries")
				type result struct {
					stdout, stderr string
					err            error
				}
				run := func(model string) result {
					stdout, stderr, err := runExampleCommand(t, []string{
						"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
						"TMPDIR=" + tmp,
						"HOME=" + tmp,
						"MCP_GATEWAY_URL=http://test.invalid/mcp",
						"MOCK_ENTERED=" + entered,
						"MOCK_RELEASE=" + release,
						"MOCK_DELIVERIES=" + deliveries,
					}, `{"session_id":"test","model":{"id":"`+model+`"}}`, "sh", "-c",
						`script=$1; shift; trap 'wait' EXIT; . "$script"`, "hook-test",
						filepath.Join(kit, "scripts", "sbx-agent-hook.sh"), "claude-status")
					return result{stdout, stderr, err}
				}
				check := func(r result) {
					t.Helper()
					require.NoError(t, r.err, r.stderr)
					require.Empty(t, r.stdout)
					require.Empty(t, r.stderr)
				}
				first := make(chan result, 1)
				go func() { first <- run("first") }()
				finished := false
				// Unblock and join the initial worker even if an assertion fails.
				t.Cleanup(func() {
					_ = os.WriteFile(release, nil, 0o644)
					if !finished {
						<-first
					}
				})
				require.Eventually(t, func() bool {
					_, err := os.Stat(entered)
					return err == nil
				}, 3*time.Second, 10*time.Millisecond)
				models := []string{"first"}
				if scenario == "newest update" {
					check(run("middle"))
					check(run("newest"))
					models = append(models, "newest")
				} else {
					check(run("first"))
				}
				writeExampleFixture(t, release, "", 0o644)
				r := <-first
				finished = true
				check(r)
				calls, err := os.ReadFile(deliveries)
				require.NoError(t, err)
				lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
				require.Len(t, lines, len(models), "only the active and newest pending status should be delivered")
				for i, model := range models {
					require.Contains(t, lines[i], `"model_id":"`+model+`"`)
				}
				// Repeating the last update must agree with the final cache.
				check(run(models[len(models)-1]))
				again, err := os.ReadFile(deliveries)
				require.NoError(t, err)
				require.Equal(t, calls, again)
				entries, err := os.ReadDir(filepath.Join(tmp, "sbx-agent-hook"))
				require.NoError(t, err)
				require.Len(t, entries, 1, "workers must remove their queue and lock after draining")
				require.Equal(t, "test_.session", entries[0].Name())
			})
		}
	}
}
