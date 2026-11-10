package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstallPreservesSettingsAndExistingHooks(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude", "settings.json")
	system := filepath.Join(t.TempDir(), "codex")
	codex := filepath.Join(system, "hooks.json")
	toml := filepath.Join(home, ".codex", "config.toml")
	initial := `{"model":"keep-this","counter":9007199254740993,"hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"validate","timeout":600}]}],"Notification":[{"hooks":[{"type":"command","command":"notify"}]}]}}`
	for _, path := range []string{claude, codex} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(initial), 0o640))
	}
	config := "notify = [\"/usr/local/bin/sbx-codex-hook\", \"codex\"]\nmodel = \"keep-this\"\n"
	require.NoError(t, os.MkdirAll(filepath.Dir(toml), 0o700))
	require.NoError(t, os.WriteFile(toml, []byte(config), 0o600))
	policy := `model = "existing-system-model"`
	require.NoError(t, os.WriteFile(filepath.Join(system, "config.toml"), []byte(policy), 0o644))
	require.NoError(t, installUserHooks(home, "both"))
	require.NoError(t, installSystemHooks(system, "both"))
	first, err := os.ReadFile(claude)
	require.NoError(t, err)
	require.NoError(t, installUserHooks(home, "both"))
	require.NoError(t, installSystemHooks(system, "both"))
	again, err := os.ReadFile(claude)
	require.NoError(t, err)
	require.Equal(t, first, again)
	for agent, path := range map[string]string{"claude": claude, "codex": codex} {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		var settings map[string]any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		require.NoError(t, decoder.Decode(&settings))
		require.Equal(t, "keep-this", settings["model"])
		require.Equal(t, json.Number("9007199254740993"), settings["counter"])
		hooks := settings["hooks"].(map[string]any)
		require.Len(t, hooks["Stop"], 2)
		require.Len(t, hooks["SessionStart"], 1)
		require.Len(t, hooks["Notification"], 1)
		registered := hooks["Stop"].([]any)[1].(map[string]any)["hooks"].([]any)[0].(map[string]any)
		require.Equal(t, installedCommand+" hook "+agent, registered["command"])
		require.NotContains(t, registered, "async", "capture must finish before the hook returns")
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	}
	unchanged, err := os.ReadFile(toml)
	require.NoError(t, err)
	require.Equal(t, config, string(unchanged), "native hooks must leave notify and other TOML settings alone")
	unchanged, err = os.ReadFile(filepath.Join(system, "config.toml"))
	require.NoError(t, err)
	require.Equal(t, policy, string(unchanged), "managed hook installation must preserve system policy")
}

func TestInstallAgentSelectionAndDefaultPermissions(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "both"} {
		t.Run(agent, func(t *testing.T) {
			home := t.TempDir()
			system := filepath.Join(t.TempDir(), "codex")
			require.NoError(t, installUserHooks(home, agent))
			require.NoError(t, installSystemHooks(system, agent))
			if agent != "codex" {
				info, err := os.Stat(filepath.Join(home, ".claude", "settings.json"))
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			} else {
				require.NoDirExists(t, filepath.Join(home, ".claude"))
			}
			require.NoDirExists(t, filepath.Join(home, ".codex"))
			if agent != "claude" {
				for _, file := range []string{"hooks.json", "config.toml"} {
					info, err := os.Stat(filepath.Join(system, file))
					require.NoError(t, err)
					require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
				}
				info, err := os.Stat(system)
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
			} else {
				require.NoDirExists(t, system)
			}
		})
	}
	require.Error(t, installUserHooks(t.TempDir(), "unknown"))
	require.Error(t, installSystemHooks(t.TempDir(), "unknown"))
}

func TestInstallFollowsExistingConfigurationSymlinks(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	target := filepath.Join(home, "managed-settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(target, []byte(`{"model":"existing"}`), 0o600))
	require.NoError(t, os.Symlink(target, path))
	require.NoError(t, installUserHooks(home, "claude"))
	link, err := os.Readlink(path)
	require.NoError(t, err)
	require.Equal(t, target, link)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Contains(t, string(data), "existing")
	require.Contains(t, string(data), "sbx-agent-checkpoint hook claude")
}

func TestInstallRejectsInvalidConfigurationWithoutReplacingIt(t *testing.T) {
	for _, input := range []string{
		`null`, `[]`, `{`, `{} {}`, `{"hooks":false}`, `{"hooks":{"Stop":{}}}`,
		`{"hooks":{"Stop":[null]}}`, `{"hooks":{"Stop":[{"hooks":true}]}}`,
		`{"hooks":{"Stop":[{"hooks":[null]}]}}`,
	} {
		t.Run(input, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, ".claude", "settings.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte(input), 0o600))
			require.Error(t, installUserHooks(home, "claude"))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, input, string(data))
		})
	}
}

func TestHooksCaptureBaselineAndTurnsForBothAgents(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			calls := make(chan checkpointRequest, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/_sbx/api/checkpoints" || r.Header.Get("Content-Type") != "application/json" {
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				var req checkpointRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, "invalid payload", http.StatusBadRequest)
					return
				}
				calls <- req
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"id":"checkpoint-1"}`)
			}))
			defer server.Close()
			for event, kind := range map[string]string{"SessionStart": "baseline", "Stop": "turn"} {
				var output bytes.Buffer
				input := fmt.Sprintf(`{"hook_event_name":%q,"session_id":"session-1","turn_id":"turn-2","last_assistant_message":"private response","prompt":"private prompt"}`, event)
				require.NoError(t, run([]string{"hook", agent}, strings.NewReader(input), &output, server.URL+"/_sbx/api/checkpoints"))
				require.JSONEq(t, `{}`, output.String())
				got := <-calls
				require.Equal(t, agent, got.Agent)
				require.Equal(t, "session-1", got.SessionID)
				require.Equal(t, "turn-2", got.TurnID)
				require.Equal(t, kind, got.Kind)
				require.Len(t, got.EventID, 64)
				payload, err := json.Marshal(got)
				require.NoError(t, err)
				require.NotContains(t, string(payload), "private")
			}
		})
	}
}

func TestHookEventIdentityTracksContinuationAndDeduplicatesRetries(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte("first turn\n"), 0o600))
	ids := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req checkpointRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		ids <- req.EventID
		_, _ = io.WriteString(w, `{"id":"saved"}`)
	}))
	defer server.Close()
	input := fmt.Sprintf(`{"hook_event_name":"Stop","session_id":"same","turn_id":"same","transcript_path":%q}`, transcript)
	send := func(input string) string {
		t.Helper()
		var output bytes.Buffer
		require.NoError(t, run([]string{"hook", "codex"}, strings.NewReader(input), &output, server.URL))
		require.JSONEq(t, `{}`, output.String())
		return <-ids
	}
	first := send(input)
	require.Equal(t, first, send(input))
	reordered := fmt.Sprintf(`{ "transcript_path":%q, "turn_id":"same", "session_id":"same", "hook_event_name":"Stop" }`, transcript)
	require.Equal(t, first, send(reordered), "JSON formatting must not change retry identity")
	require.NoError(t, os.WriteFile(transcript, []byte("second turn\n"), 0o600))
	require.NotEqual(t, first, send(input), "a continued turn must capture its changed state even with the same native turn id")
}

func TestHooksReportFailuresWithoutBlockingTheAgent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		message string
	}{
		{"denied", http.StatusForbidden, `{"message":"sandbox API capability not granted"}`, "HTTP 403: sandbox API capability not granted"},
		{"unsupported", http.StatusNotImplemented, `{"message":"checkpoint cannot capture writable external mount at \"/workspace\"; recreate with --clone"}`, `HTTP 501: checkpoint cannot capture writable external mount at "/workspace"; recreate with --clone`},
		{"failed", http.StatusInternalServerError, "private server detail", "HTTP 500"},
		{"invalid error", http.StatusNotImplemented, `{"message":`, "HTTP 501"},
		{"oversized error", http.StatusNotImplemented, `{"message":"private server detail"}` + strings.Repeat(" ", 64<<10), "HTTP 501"},
		{"oversized message", http.StatusNotImplemented, `{"message":"private server detail` + strings.Repeat("x", 1024) + `"}`, "HTTP 501"},
		{"empty error", http.StatusNotImplemented, `{"message":" "}`, "HTTP 501"},
		{"missing id", http.StatusCreated, `{}`, "checkpoint response has no checkpoint id"},
		{"invalid response", http.StatusOK, "not JSON", "checkpoint response has no checkpoint id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			var output bytes.Buffer
			input := `{"hook_event_name":"Stop","session_id":"session"}`
			require.NoError(t, run([]string{"hook", "claude"}, strings.NewReader(input), &output, server.URL))
			var response map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &response))
			prefix := "Filesystem checkpoint was not saved: "
			if tc.status >= 300 {
				prefix += "checkpoint request returned "
			}
			require.Equal(t, prefix+tc.message, response["systemMessage"])
			require.NotContains(t, response, "decision")
			require.NotContains(t, response, "continue")
			require.NotContains(t, output.String(), "private server detail")
		})
	}
}

func TestHookRejectsRedirects(t *testing.T) {
	redirected := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected <- struct{}{}
		_, _ = io.WriteString(w, `{"id":"wrong-host"}`)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	var output bytes.Buffer
	require.NoError(t, run([]string{"hook", "codex"}, strings.NewReader(`{"hook_event_name":"Stop","session_id":"s"}`), &output, server.URL))
	require.Contains(t, output.String(), "HTTP 307")
	select {
	case <-redirected:
		t.Fatal("a checkpoint hook must not forward its request to a redirect target")
	default:
	}
}

func TestHookIgnoresSubagentAndToolEvents(t *testing.T) {
	for _, event := range []string{"SubagentStop", "PostToolUse", "Notification", "Interrupt"} {
		var output bytes.Buffer
		input := fmt.Sprintf(`{"hook_event_name":%q,"session_id":"s"}`, event)
		require.NoError(t, run([]string{"hook", "claude"}, strings.NewReader(input), &output, "http://unreachable.invalid"))
		require.JSONEq(t, `{}`, output.String())
	}
}

func TestHookInputFailuresRemainNonblocking(t *testing.T) {
	for _, input := range []string{
		`{`, `{"hook_event_name":"Stop"}`, `{"hook_event_name":"Stop","session_id":"s"} {}`,
		strings.Repeat("x", maxHookInput+1),
		`{"hook_event_name":"Stop","session_id":"s","transcript_path":"/missing/transcript"}`,
	} {
		var output bytes.Buffer
		require.NoError(t, run([]string{"hook", "claude"}, strings.NewReader(input), &output, "http://unreachable.invalid"))
		var response map[string]any
		require.NoError(t, json.Unmarshal(output.Bytes(), &response))
		require.Contains(t, response["systemMessage"], "checkpoint was not saved")
		require.NotContains(t, response, "decision")
	}
}

func TestHookRejectsFIFOTranscriptBeforeOpeningIt(t *testing.T) {
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo is unavailable")
	}
	path := filepath.Join(t.TempDir(), "transcript")
	require.NoError(t, exec.Command(mkfifo, path).Run())
	input := fmt.Sprintf(`{"hook_event_name":"Stop","session_id":"s","transcript_path":%q}`, path)
	var output bytes.Buffer
	require.NoError(t, run([]string{"hook", "codex"}, strings.NewReader(input), &output, "http://unreachable.invalid"))
	require.Contains(t, output.String(), "turn transcript is not a regular file")
}
