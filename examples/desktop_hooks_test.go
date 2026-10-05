package examples_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
	"github.com/stretchr/testify/require"
)

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

func TestDesktopHookRetriesFailedStatusDelivery(t *testing.T) {
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
