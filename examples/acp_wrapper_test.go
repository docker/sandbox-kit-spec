package examples_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func runClaudeACPWrapper(t *testing.T, adapterSource, input string, env []string) (string, string, string, error) {
	t.Helper()
	node := requireExampleTool(t, "node")
	root := t.TempDir()
	adapter := filepath.Join(root, "adapter.mjs")
	writeExampleFixture(t, adapter, adapterSource, 0o644)
	wrapper, err := os.ReadFile(filepath.Join("claude-acp", "scripts", "claude-agent-acp-wrapper.mjs"))
	require.NoError(t, err)
	installed := "/opt/claude-agent-acp/lib/node_modules/@agentclientprotocol/claude-agent-acp/dist/index.js"
	require.Equal(t, 1, strings.Count(string(wrapper), installed))
	path := filepath.Join(root, "wrapper.mjs")
	writeExampleFixture(t, path, strings.Replace(string(wrapper), installed, adapter, 1), 0o644)
	claude := filepath.Join(root, "claude")
	calls := filepath.Join(root, "calls")
	writeExampleFixture(t, claude, `#!/bin/sh
printf '%s\n' "$*" >> "$MOCK_CALLS"
printf '%s' "$MOCK_AUTH"
exit "${MOCK_STATUS:-0}"
`, 0o755)
	env = append(env, "CLAUDE_CODE_EXECUTABLE="+claude, "MOCK_CALLS="+calls)
	stdout, stderr, err := runExampleCommand(t, env, input, node, path)
	var probes string
	if data, readErr := os.ReadFile(calls); readErr == nil {
		probes = string(data)
	} else {
		require.True(t, os.IsNotExist(readErr), "%v", readErr)
	}
	return stdout, stderr, probes, err
}

func TestClaudeACPAuthNotificationFallback(t *testing.T) {
	const notification = `{"method":"_auth/status_update","params":{"authStatus":{"kind":"none"}}}`
	adapter := `process.stdout.write(` + "`" + notification + "\n`" + `);
process.stdin.resume();`
	for _, tc := range []struct{ name, auth, status, want string }{
		{"authenticated", `{"loggedIn":true}`, "0", ""},
		{"logged out", `{"loggedIn":false}`, "0", notification + "\n"},
		{"malformed", `{`, "0", notification + "\n"},
		{"null", `null`, "0", notification + "\n"},
		{"failed probe", `{"loggedIn":true}`, "1", notification + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, probes, err := runClaudeACPWrapper(t, adapter, "", []string{"MOCK_AUTH=" + tc.auth, "MOCK_STATUS=" + tc.status})
			require.NoError(t, err, stderr)
			require.Empty(t, stderr)
			require.Equal(t, tc.want, stdout)
			require.Equal(t, "auth status --json\n", probes)
		})
	}
}

func TestClaudeACPExitDrainsOutput(t *testing.T) {
	const adapter = `process.stdin.resume();
process.stdin.on("end", () => {
  process.stdout.write(JSON.stringify({method:"test/final", params:{body:"x".repeat(100000)}}) + "\n", () => {
    if (process.env.MOCK_SIGNAL === "1") process.kill(process.pid, "SIGTERM");
    else process.exit(Number(process.env.MOCK_EXIT));
  });
});`
	for _, tc := range []struct {
		name, code, signal string
		wantCode           int
	}{
		{"success", "0", "0", 0},
		{"failure", "7", "0", 7},
		{"signal", "0", "1", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, probes, err := runClaudeACPWrapper(t, adapter, "", []string{"MOCK_EXIT=" + tc.code, "MOCK_SIGNAL=" + tc.signal})
			require.Empty(t, stderr)
			require.Empty(t, probes)
			if tc.wantCode == 0 {
				require.NoError(t, err)
			} else {
				var exitErr *exec.ExitError
				require.True(t, errors.As(err, &exitErr), "%v", err)
				require.Equal(t, tc.wantCode, exitErr.ExitCode())
			}
			var message struct {
				Method string                `json:"method"`
				Params struct{ Body string } `json:"params"`
			}
			require.NoError(t, json.Unmarshal([]byte(stdout), &message))
			require.Equal(t, "test/final", message.Method)
			require.Equal(t, strings.Repeat("x", 100000), message.Params.Body)
		})
	}
}
