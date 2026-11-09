package spec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexExampleAuthenticationModes(t *testing.T) {
	for _, name := range []string{"codex", "codex-mixin"} {
		for _, mode := range []string{"oauth", "apikey", "none"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				raw, err := os.ReadFile(filepath.Join("..", "examples", name, name+".yaml"))
				require.NoError(t, err)
				d, err := Decode(raw)
				require.NoError(t, err)
				values, err := ResolveArgs(d.Args, nil)
				require.NoError(t, err)
				raw, err = ExpandCreateArgs(raw, d.Args, values)
				require.NoError(t, err)
				d, err = Decode(raw)
				require.NoError(t, err)
				lifecycle, err := LifecycleOf(d.Capabilities)
				require.NoError(t, err)
				home := t.TempDir()
				auth := filepath.Join(home, ".codex", "auth.json")
				require.NoError(t, os.MkdirAll(filepath.Dir(auth), 0700))
				require.NoError(t, os.WriteFile(auth, []byte(`{"OPENAI_API_KEY":"stale"}`), 0600))
				runInstall := func() {
					for _, hook := range lifecycle.Install {
						argv := append([]string{}, hook.Command...)
						for i := range argv {
							argv[i] = strings.ReplaceAll(argv[i], "/home/agent", home)
						}
						cmd := exec.Command(argv[0], argv[1:]...)
						cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "SBX_CRED_OPENAI_MODE=" + mode}
						output, err := cmd.CombinedOutput()
						require.NoError(t, err, "%s", output)
					}
				}
				runInstall()
				config, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
				require.NoError(t, err)
				require.NotContains(t, string(config), "requires_openai_auth = false")
				switch mode {
				case "oauth":
					require.Contains(t, string(config), `forced_login_method = "chatgpt"`)
					require.Contains(t, string(config), "apps = true")
					require.NoFileExists(t, auth, "the host renderer must be able to seed the native cache")
					native := []byte(`{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"masked"}}`)
					require.NoError(t, os.WriteFile(auth, native, 0600))
					runInstall()
					preserved, err := os.ReadFile(auth)
					require.NoError(t, err)
					require.Equal(t, native, preserved)
				case "apikey":
					require.Contains(t, string(config), `forced_login_method = "api"`)
					cache, err := os.ReadFile(auth)
					require.NoError(t, err)
					require.JSONEq(t, `{"OPENAI_API_KEY":"proxy-managed"}`, string(cache))
				default:
					require.NotContains(t, string(config), "forced_login_method")
					require.NoFileExists(t, auth)
				}
			})
		}
	}
}
