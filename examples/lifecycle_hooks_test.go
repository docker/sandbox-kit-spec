package examples_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/stretchr/testify/require"
)

func TestClaudeMCPRegistrationHooks(t *testing.T) {
	for _, kit := range []string{"claude", "claude-mixin"} {
		t.Run(kit, func(t *testing.T) {
			lifecycle, err := spec.LifecycleOf(exampleDescriptor(t, kit).Capabilities)
			require.NoError(t, err)
			require.NotNil(t, lifecycle)
			require.NotEmpty(t, lifecycle.Install)
			require.NotEmpty(t, lifecycle.Startup)
			install := lifecycle.Install[len(lifecycle.Install)-1]
			startup := lifecycle.Startup[len(lifecycle.Startup)-1]
			require.Equal(t, install.Command, startup.Command)
			require.Equal(t, "agent", install.User)
			require.Equal(t, install.User, startup.User)
			require.Equal(t, []string{"MCP_GATEWAY_URL", "MCP_SENTINEL_TOKEN_NAME"}, install.Env)
			require.Equal(t, install.Env, startup.Env)
			require.False(t, startup.Background)

			for _, tc := range []struct {
				name, url, status string
			}{
				{"disabled", "", "0"},
				{"registered", "http://test.invalid/mcp", "0"},
				{"already registered", "http://test.invalid/mcp", "1"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					root := t.TempDir()
					calls := filepath.Join(root, "calls")
					bin := filepath.Join(root, "bin")
					writeExampleFixture(t, filepath.Join(bin, "claude"), `#!/bin/sh
printf '%s\n' "$@" >> "$MOCK_CALLS"
exit "$MOCK_STATUS"
`, 0o755)
					for _, command := range []spec.CommandLine{install.Command, startup.Command} {
						stdout, stderr, err := runExampleCommand(t, []string{
							"HOME=" + root,
							"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
							"MCP_GATEWAY_URL=" + tc.url,
							"MCP_SENTINEL_TOKEN_NAME=test-token",
							"MOCK_CALLS=" + calls,
							"MOCK_STATUS=" + tc.status,
						}, "", command...)
						require.NoError(t, err, stderr)
						require.Empty(t, stdout)
						require.Empty(t, stderr)
					}
					if tc.url == "" {
						require.NoFileExists(t, calls)
						return
					}
					got, err := os.ReadFile(calls)
					require.NoError(t, err)
					want := "mcp\nadd\nmcp-gateway\n" + tc.url + "\n--transport\nhttp\n--scope\nuser\n--header\nAuthorization: Bearer test-token\n"
					require.Equal(t, want+want, string(got), "both phases must register without removing an existing entry")
				})
			}
		})
	}
}
