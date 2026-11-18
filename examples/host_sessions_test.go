package examples_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/resolve"
	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/stretchr/testify/require"
)

func hostSessionHook(t *testing.T, agent, home, history string) []string {
	t.Helper()
	lifecycle, err := spec.LifecycleOf(exampleDescriptor(t, agent+"-sessions").Capabilities)
	require.NoError(t, err)
	require.Len(t, lifecycle.Install, 1)
	argv := append([]string(nil), lifecycle.Install[0].Command...)
	// Exercise the published hook in isolated homes without touching the
	// running agent's own history. The shared directory outlives each home.
	for i, arg := range argv {
		arg = strings.ReplaceAll(arg, "/home/agent/"+agent+"-history", history)
		argv[i] = strings.ReplaceAll(arg, "/home/agent", home)
	}
	return argv
}

func TestHostSessionsSurviveHomeReplacement(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			history := filepath.Join(t.TempDir(), "shared history")
			var names []string
			if agent == "codex" {
				names = []string{"sessions", "archived_sessions"}
			} else {
				names = []string{"projects", "sessions", "todos", "shell-snapshots", "statsig"}
			}
			for generation := range 2 {
				home := filepath.Join(t.TempDir(), "agent home")
				state := filepath.Join(home, "."+agent)
				writeExampleFixture(t, filepath.Join(state, "settings.json"), "kit config", 0o600)
				writeExampleFixture(t, filepath.Join(state, "auth.json"), "kit credential", 0o600)
				if generation == 0 {
					// Empty directories shipped by a base image can be replaced.
					for _, name := range names {
						require.NoError(t, os.MkdirAll(filepath.Join(state, name), 0o700))
					}
				}
				for range 2 {
					_, stderr, err := runExampleCommand(t, nil, "", hostSessionHook(t, agent, home, history)...)
					require.NoError(t, err, stderr)
				}
				for _, name := range names {
					path := filepath.Join(state, name, "retained")
					if generation == 0 {
						require.NoError(t, os.WriteFile(path, []byte(name), 0o600))
					} else {
						data, err := os.ReadFile(path)
						require.NoError(t, err)
						require.Equal(t, name, string(data))
					}
				}
				if agent == "codex" && generation == 0 {
					// os.Rename never copies on EXDEV, unlike mv. Codex needs
					// the two symlinks to resolve onto the same filesystem.
					from := filepath.Join(state, "sessions", "rollout.jsonl")
					to := filepath.Join(state, "archived_sessions", "rollout.jsonl")
					require.NoError(t, os.WriteFile(from, []byte("transcript"), 0o600))
					require.NoError(t, os.Rename(from, to))
				}
				for _, name := range []string{"settings.json", "auth.json"} {
					info, err := os.Lstat(filepath.Join(state, name))
					require.NoError(t, err)
					require.True(t, info.Mode().IsRegular())
					_, err = os.Stat(filepath.Join(history, name))
					require.True(t, os.IsNotExist(err))
				}
				require.NoError(t, os.RemoveAll(home))
			}
		})
	}
}

func TestHostSessionsRefuseExistingState(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		for _, kind := range []string{"nonempty directory", "file", "unexpected symlink", "dangling symlink"} {
			t.Run(agent+"/"+kind, func(t *testing.T) {
				home := t.TempDir()
				history := filepath.Join(t.TempDir(), "history")
				path := filepath.Join(home, "."+agent, "sessions")
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				var retained string
				switch kind {
				case "nonempty directory":
					retained = filepath.Join(path, "rollout.jsonl")
					writeExampleFixture(t, retained, "keep", 0o600)
				case "file":
					retained = path
					writeExampleFixture(t, retained, "keep", 0o600)
				default:
					retained = filepath.Join(home, "other")
					if kind == "unexpected symlink" {
						writeExampleFixture(t, retained, "keep", 0o600)
					}
					require.NoError(t, os.Symlink(retained, path))
				}
				_, stderr, err := runExampleCommand(t, nil, "", hostSessionHook(t, agent, home, history)...)
				require.Error(t, err)
				require.Contains(t, stderr, path)
				if strings.Contains(kind, "symlink") {
					target, err := os.Readlink(path)
					require.NoError(t, err)
					require.Equal(t, retained, target)
				}
				if kind != "dangling symlink" {
					data, err := os.ReadFile(retained)
					require.NoError(t, err)
					require.Equal(t, "keep", string(data))
				}
			})
		}
	}
}

func TestHostSessionsComposeAfterAgentSetup(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		for _, variant := range []string{agent, agent + "-mixin"} {
			t.Run(variant, func(t *testing.T) {
				kits := []string{agent + "-sessions", variant}
				if strings.HasSuffix(variant, "-mixin") {
					kits = append(kits, "shell")
				}
				var units []*resolve.Unit
				for _, kit := range kits {
					raw, err := os.ReadFile(filepath.Join(kit, kit+".yaml"))
					require.NoError(t, err)
					d := exampleDescriptor(t, kit)
					values, err := spec.ResolveArgs(d.Args, nil)
					require.NoError(t, err)
					raw, err = spec.ExpandBuildArgs(raw, d.Args, values)
					require.NoError(t, err)
					raw, err = spec.ExpandCreateArgs(raw, d.Args, values)
					require.NoError(t, err)
					d, err = spec.Decode(raw)
					require.NoError(t, err)
					selected, err := spec.SelectCapabilities(t.Context(), kit, d, func(_ context.Context, _ spec.SelectionTarget, _ spec.Descriptor, c spec.Capability) spec.CapabilityDecision {
						return spec.CapabilityDecision{Accepted: c.Type != spec.CapabilityAgentContext}
					})
					require.NoError(t, err)
					d.Capabilities = selected.Capabilities
					units = append(units, &resolve.Unit{Reference: kit, Descriptor: d})
				}
				resolution, err := resolve.Resolve(units)
				require.NoError(t, err)
				var contributions []spec.Contribution
				for _, unit := range resolution.Topological() {
					contributions = append(contributions, spec.Contribution{Reference: unit.Reference, Descriptor: unit.Descriptor})
				}
				composed, err := spec.Compose(contributions)
				require.NoError(t, err)
				lifecycles, err := spec.LifecyclesOf(composed.Capabilities)
				require.NoError(t, err)
				var installs []spec.InstallHook
				for _, lifecycle := range lifecycles {
					installs = append(installs, lifecycle.Install...)
				}
				require.Greater(t, len(installs), 1)
				last := installs[len(installs)-1]
				require.Contains(t, last.Description, "host-shared directory")
				if agent == "claude" {
					for _, c := range composed.Capabilities {
						if c.Type == spec.CapabilityVolume {
							require.NotContains(t, c.Config["path"], "/home/agent/.claude/")
						}
					}
				}
			})
		}
	}
}
