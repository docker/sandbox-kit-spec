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
