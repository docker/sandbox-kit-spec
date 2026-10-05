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
