package examples_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/stretchr/testify/require"
)

func exampleDescriptor(t *testing.T, kit string) *spec.Descriptor {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(kit, kit+".yaml"))
	require.NoError(t, err)
	d, err := spec.Decode(raw)
	require.NoError(t, err)
	return d
}

func writeExampleFixture(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), mode))
}

func runExampleCommand(t *testing.T, env []string, input string, argv ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func requireExampleTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("example script requires %s: %v", name, err)
	}
	return path
}

func TestAgentSessionPromptIsHeadless(t *testing.T) {
	for kit, prefix := range map[string]string{"claude": "-p", "codex": "exec"} {
		t.Run(kit, func(t *testing.T) {
			sessions, err := spec.AgentSessionsOf(exampleDescriptor(t, kit).Capabilities)
			require.NoError(t, err)
			require.NotNil(t, sessions)
			require.Equal(t, []string{prefix, spec.SessionPromptPlaceholder}, sessions.Prompt)
		})
	}
	for _, kit := range []string{"claude-mixin", "codex-mixin"} {
		t.Run(kit, func(t *testing.T) {
			sessions, err := spec.AgentSessionsOf(exampleDescriptor(t, kit).Capabilities)
			require.NoError(t, err)
			require.Nil(t, sessions, "a mixin must leave the workload's session surface alone")
		})
	}
}

func TestClaudeSessionListEmitsResumableIDs(t *testing.T) {
	node := requireExampleTool(t, "node")
	root := t.TempDir()
	script, err := os.ReadFile(filepath.Join("claude", "scripts", "interactive-sessions.mjs"))
	require.NoError(t, err)
	path := filepath.Join(root, "interactive-sessions.mjs")
	writeExampleFixture(t, path, string(script), 0o644)
	sdk := filepath.Join(root, "node_modules", "@anthropic-ai", "claude-agent-sdk")
	writeExampleFixture(t, filepath.Join(sdk, "package.json"), `{"type":"module","exports":"./index.mjs"}`, 0o644)
	writeExampleFixture(t, filepath.Join(sdk, "index.mjs"), `export async function listSessions() { return JSON.parse(process.env.KIT_TEST_SESSIONS); }`, 0o644)
	bin := filepath.Join(root, "bin")
	writeExampleFixture(t, filepath.Join(bin, "claude"), "#!/bin/sh\nprintf '%s' \"$KIT_TEST_BACKGROUNDS\"\n", 0o755)

	for _, tc := range []struct {
		name, sessions, background, cwd, want string
	}{
		{"empty", `[]`, `[]`, "", ""},
		{
			name:       "sorted without background agents",
			sessions:   `[{"sessionId":"old","lastModified":1},{"sessionId":"background","lastModified":4},{"sessionId":"new","lastModified":3,"cwd":"/project"},{"sessionId":"middle","lastModified":2,"cwd":"/other"}]`,
			background: `[{"kind":"background","sessionId":"background"},{"kind":"interactive","sessionId":"new"}]`,
			want:       "new\nmiddle\nold\n",
		},
		{
			name:       "exact cwd filter",
			sessions:   `[{"sessionId":"keep","lastModified":1,"cwd":"/project"},{"sessionId":"sibling","lastModified":2,"cwd":"/project-other"},{"sessionId":"unknown","lastModified":3}]`,
			background: `[]`,
			cwd:        "/project",
			want:       "keep\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv := []string{node, path}
			if tc.cwd != "" {
				argv = append(argv, tc.cwd)
			}
			stdout, stderr, err := runExampleCommand(t, []string{
				"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"KIT_TEST_SESSIONS=" + tc.sessions,
				"KIT_TEST_BACKGROUNDS=" + tc.background,
			}, "", argv...)
			require.NoError(t, err, stderr)
			require.Empty(t, stderr)
			require.Equal(t, tc.want, stdout)
		})
	}
}

func TestCodexSessionListEmitsSortedIDsAcrossPages(t *testing.T) {
	requireExampleTool(t, "jq")
	sessions, err := spec.AgentSessionsOf(exampleDescriptor(t, "codex").Capabilities)
	require.NoError(t, err)
	require.NotNil(t, sessions)
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	// Interleaved notifications exercise the ID matching loop. Active and
	// archived pages deliberately overlap in time to catch per-page sorting.
	writeExampleFixture(t, filepath.Join(bin, "codex"), `#!/bin/sh
while IFS= read -r request; do
  method=$(printf '%s' "$request" | jq -r '.method')
  id=$(printf '%s' "$request" | jq -r '.id // empty')
  [ -n "$id" ] || continue
  if [ "${KIT_TEST_RPC_ERROR:-}" = 1 ]; then
    jq -nc --argjson id "$id" '{id: $id, error: {message: "test error"}}'
    continue
  fi
  result='{}'
  if [ "$method" = thread/list ]; then
    page=$(printf '%s' "$request" | jq -r '[(.params.archived | tostring), (.params.cursor // "start")] | join("-")')
    case "$page" in
      false-start) result='{"data":[{"id":"old","updatedAt":1,"source":"cli"}],"nextCursor":"next"}' ;;
      false-next) result='{"data":[{"id":"new","updatedAt":3,"source":"exec"}],"nextCursor":null}' ;;
      true-start) result='{"data":[{"id":"middle","updatedAt":2,"source":"appServer"}],"nextCursor":null}' ;;
      *) exit 1 ;;
    esac
    if [ "${KIT_TEST_SOURCE_MIX:-}" = 1 ]; then
      case "$page" in
        false-start) result='{"data":[{"id":"old","updatedAt":1,"source":"cli"},{"id":"worker","updatedAt":10,"source":"subAgent"},{"id":"unknown","updatedAt":11,"source":"unknown"}],"nextCursor":"next"}' ;;
        false-next) result='{"data":[{"id":"new","updatedAt":3,"source":"exec"},{"id":"review","updatedAt":12,"source":"subAgentReview"},{"id":"compact","updatedAt":13,"source":"subAgentCompact"}],"nextCursor":null}' ;;
        true-start) result='{"data":[{"id":"middle","updatedAt":2,"source":"appServer"},{"id":"editor","updatedAt":4,"source":"vscode"},{"id":"spawn","updatedAt":14,"source":"subAgentThreadSpawn"},{"id":"other","updatedAt":15,"source":"subAgentOther"}],"nextCursor":null}' ;;
      esac
    fi
    # Simulate server-side source filtering so an overbroad query exposes
    # background threads in stdout rather than merely failing a mock pin.
    sources=$(printf '%s' "$request" | jq -c '.params.sourceKinds')
    result=$(printf '%s' "$result" | jq -c --argjson sources "$sources" \
      '.data |= map(select(.source as $kind | $sources | index($kind)))')
    if [ "${KIT_TEST_EMPTY:-}" = 1 ]; then
      result='{"data":[],"nextCursor":null}'
    fi
  fi
  printf '%s\n' '{"method":"test/notification","params":{}}'
  jq -nc --argjson id "$id" --argjson result "$result" '{id: $id, result: $result}'
done
`, 0o755)
	for _, tc := range []struct {
		name, env, want string
		fails           bool
	}{
		{"paginated", "KIT_TEST_EMPTY=0", "new\nmiddle\nold\n", false},
		{"user resumable sources", "KIT_TEST_SOURCE_MIX=1", "editor\nnew\nmiddle\nold\n", false},
		{"empty", "KIT_TEST_EMPTY=1", "", false},
		{"rpc error", "KIT_TEST_RPC_ERROR=1", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			stdout, stderr, err := runExampleCommand(t, []string{
				"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"TMPDIR=" + tmp,
				"KIT_TEST_RPC_ERROR=0",
				"KIT_TEST_EMPTY=0",
				"KIT_TEST_SOURCE_MIX=0",
				tc.env,
			}, "", sessions.List...)
			if tc.fails {
				require.Error(t, err)
				require.Contains(t, stderr, "test error")
			} else {
				require.NoError(t, err, stderr)
				require.Empty(t, stderr)
			}
			require.Equal(t, tc.want, stdout)
			entries, err := os.ReadDir(tmp)
			require.NoError(t, err)
			require.Empty(t, entries, "the app-server FIFOs must be removed on success and failure")
		})
	}
}
