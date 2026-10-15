package examples_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/stretchr/testify/require"
)

func TestClaudeSettingsSeed(t *testing.T) {
	for _, kit := range []string{"claude", "claude-mixin"} {
		t.Run(kit, func(t *testing.T) {
			descriptor := exampleDescriptor(t, kit)
			lifecycle, err := spec.LifecycleOf(descriptor.Capabilities)
			require.NoError(t, err)
			require.NotNil(t, lifecycle)
			require.GreaterOrEqual(t, len(lifecycle.Install), 3)
			hook := lifecycle.Install[2]
			require.Equal(t, "agent", hook.User)
			require.Equal(t, []string{"SBX_CRED_ANTHROPIC_MODE"}, hook.Env)
			require.Len(t, hook.Command, 3)
			require.Equal(t, []string{"sh", "-c"}, []string(hook.Command[:2]))

			for _, mode := range []string{"none", "apikey", "oauth"} {
				for _, model := range []string{"", "claude-test"} {
					for _, effort := range descriptor.Args["reasoningEffort"].Enum {
						t.Run(mode+"/"+model+"/"+effort, func(t *testing.T) {
							root := t.TempDir()
							path := filepath.Join(root, "settings.json")
							writeExampleFixture(t, path, `{"stale":true}`, 0o600)
							values, err := spec.ResolveArgs(descriptor.Args, map[string]string{"model": model, "reasoningEffort": effort})
							require.NoError(t, err)
							expanded, err := spec.ExpandLifecycle(lifecycle, values)
							require.NoError(t, err)
							command := append([]string(nil), expanded.Install[2].Command...)
							for i := range command {
								command[i] = strings.ReplaceAll(command[i], "/home/agent/.claude/settings.json", path)
							}
							stdout, stderr, err := runExampleCommand(t, []string{"SBX_CRED_ANTHROPIC_MODE=" + mode}, "", command...)
							require.NoError(t, err, stderr)
							require.Empty(t, stdout)
							require.Empty(t, stderr)
							got, err := os.ReadFile(path)
							require.NoError(t, err)
							want := map[string]any{
								"themeId":                           1,
								"alwaysThinkingEnabled":             true,
								"permissions":                       map[string]string{"defaultMode": "bypassPermissions"},
								"bypassPermissionsModeAccepted":     true,
								"skipDangerousModePermissionPrompt": true,
								"statusLine":                        map[string]string{"type": "command", "command": "/usr/local/bin/sbx-claude-hook claude-status"},
							}
							if mode != "none" {
								want["apiKeyHelper"] = "echo proxy-managed"
							}
							if model != "" {
								want["model"] = model
							}
							switch effort {
							case "":
							case "max", "auto":
								want["env"] = map[string]string{"CLAUDE_CODE_EFFORT_LEVEL": effort}
							default:
								want["effortLevel"] = effort
							}
							hooks := map[string]any{}
							for _, event := range []string{"Stop", "StopFailure", "UserPromptSubmit", "Notification"} {
								entry := map[string]any{"hooks": []any{map[string]any{
									"type": "command", "command": "/usr/local/bin/sbx-claude-hook claude", "timeout": 10, "async": true,
								}}}
								if event == "Notification" {
									entry["matcher"] = "permission_prompt|idle_prompt|elicitation_dialog|elicitation_url_dialog|agent_needs_input"
								}
								hooks[event] = []any{entry}
							}
							want["hooks"] = hooks
							encoded, err := json.Marshal(want)
							require.NoError(t, err)
							require.JSONEq(t, string(encoded), string(got))
							info, err := os.Stat(path)
							require.NoError(t, err)
							require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "rewriting settings must preserve permissions")
						})
					}
				}
			}
		})
	}
}
