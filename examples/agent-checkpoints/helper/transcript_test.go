package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func captureTurnRequest(t *testing.T, agent, transcript string, fields map[string]any) checkpointRequest {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(transcript), 0o600))
	input := map[string]any{"hook_event_name": "Stop", "session_id": "session", "turn_id": "current", "transcript_path": path}
	for key, value := range fields {
		input[key] = value
	}
	requests := make(chan checkpointRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request checkpointRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests <- request
		_, _ = io.WriteString(w, `{"id":"saved"}`)
	}))
	defer server.Close()
	data, err := json.Marshal(input)
	require.NoError(t, err)
	var output bytes.Buffer
	require.NoError(t, run([]string{"hook", agent}, bytes.NewReader(data), &output, server.URL))
	require.JSONEq(t, `{}`, output.String())
	return <-requests
}

func TestTurnCheckpointCapturesClaudePromptAndResponse(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"user","message":{"content":"previous prompt"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"previous response"}]}}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"change the file"},{"type":"image","source":"private image"},{"type":"text","text":"and test it"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"private tool output"},{"type":"text","text":"private tool annotation"}]}}`,
		`{"type":"user","isMeta":true,"message":{"content":"private metadata"}}`,
		`{"type":"user","isCompactSummary":true,"message":{"content":"private compact summary"}}`,
		`{"type":"user","isSidechain":true,"message":{"content":"private subagent prompt"}}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"private reasoning"},{"type":"text","text":"transcript response"}]}}`,
	}, "\n")
	for _, tc := range []struct {
		name     string
		fields   map[string]any
		response string
	}{
		{"transcript response", nil, "transcript response"},
		{"hook response", map[string]any{"last_assistant_message": "hook response"}, "hook response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := captureTurnRequest(t, "claude", transcript, tc.fields)
			require.Equal(t, "current", request.TurnID)
			require.Equal(t, &checkpointTurnInfo{Prompt: new("change the file\nand test it"), Response: new(tc.response)}, request.Turn)
			payload, err := json.Marshal(request)
			require.NoError(t, err)
			require.NotContains(t, string(payload), "private")
		})
	}
}

func TestTurnCheckpointCapturesCodexPromptAndResponse(t *testing.T) {
	for _, tc := range []struct {
		name       string
		transcript string
	}{
		{"events", strings.Join([]string{
			`{"type":"event_msg","payload":{"type":"task_started","turn_id":"previous"}}`,
			`{"type":"event_msg","payload":{"type":"user_message","message":"previous prompt"}}`,
			`{"type":"event_msg","payload":{"type":"task_started","turn_id":"current"}}`,
			`{"type":"event_msg","payload":{"type":"user_message","message":"change the file"}}`,
			`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"private injected context"}]}}`,
			`{"type":"turn_context","payload":{"turn_id":"current"}}`,
			`{"type":"event_msg","payload":{"type":"agent_reasoning","message":"private reasoning"}}`,
			`{"type":"response_item","payload":{"type":"function_call_output","output":"private tool output"}}`,
			`{"type":"event_msg","payload":{"type":"agent_message","phase":"commentary","message":"private progress"}}`,
			`{"type":"event_msg","payload":{"type":"agent_message","phase":"final_answer","message":"transcript response"}}`,
		}, "\n")},
		{"response items", strings.Join([]string{
			`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"previous prompt"}]}}`,
			`{"type":"turn_context","payload":{"turn_id":"previous"}}`,
			`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"change the file"}]}}`,
			`{"type":"turn_context","payload":{"turn_id":"current"}}`,
			`{"type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"private reasoning"}]}}`,
			`{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"private progress"}]}}`,
			`{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"transcript response"}]}}`,
		}, "\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, direct := range []bool{false, true} {
				fields := map[string]any{}
				response := "transcript response"
				if direct {
					response = "hook response"
					fields["last_assistant_message"] = response
				}
				request := captureTurnRequest(t, "codex", tc.transcript, fields)
				require.Equal(t, &checkpointTurnInfo{Prompt: new("change the file"), Response: new(response)}, request.Turn)
				payload, err := json.Marshal(request)
				require.NoError(t, err)
				require.NotContains(t, string(payload), "private")
			}
		})
	}
}

func TestTurnCheckpointDoesNotReuseAnEarlierPrompt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		agent      string
		transcript string
	}{
		{"codex turn start", "codex", `{"type":"event_msg","payload":{"type":"user_message","message":"previous prompt"}}` + "\n" + `{"type":"event_msg","payload":{"type":"turn_started","turn_id":"current"}}`},
		{"codex turn context", "codex", `{"type":"event_msg","payload":{"type":"user_message","message":"previous prompt"}}` + "\n" + `{"type":"turn_context","payload":{"turn_id":"previous"}}` + "\n" + `{"type":"turn_context","payload":{"turn_id":"current"}}`},
		{"codex mismatched turn", "codex", `{"type":"event_msg","payload":{"type":"task_started","turn_id":"previous"}}` + "\n" + `{"type":"event_msg","payload":{"type":"user_message","message":"previous prompt"}}`},
		{"claude image prompt", "claude", `{"type":"user","message":{"content":"previous prompt"}}` + "\n" + `{"type":"user","message":{"content":[{"type":"image","source":"image"}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := captureTurnRequest(t, tc.agent, tc.transcript, map[string]any{"last_assistant_message": "current response"})
			require.Equal(t, &checkpointTurnInfo{Response: new("current response")}, request.Turn)
		})
	}
}

func TestTurnCheckpointBoundsTextAtUTF8Boundary(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			prompt := strings.Repeat("a", 16383) + "🙂tail"
			var entry any = map[string]any{"type": "user", "message": map[string]any{"content": prompt}}
			if agent == "codex" {
				entry = map[string]any{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": prompt}}
			}
			transcript, err := json.Marshal(entry)
			require.NoError(t, err)
			request := captureTurnRequest(t, agent, string(transcript), map[string]any{"last_assistant_message": strings.Repeat("🙂", 4097)})
			require.NotNil(t, request.Turn)
			require.Equal(t, strings.Repeat("a", 16383), *request.Turn.Prompt)
			require.Equal(t, strings.Repeat("🙂", 4096), *request.Turn.Response)
			require.True(t, utf8.ValidString(*request.Turn.Prompt))
			require.True(t, utf8.ValidString(*request.Turn.Response))
			require.True(t, request.Turn.Truncated)
		})
	}
	request := captureTurnRequest(t, "claude", "", map[string]any{"last_assistant_message": strings.Repeat("a", 16384)})
	require.False(t, request.Turn.Truncated)
	require.Len(t, *request.Turn.Response, 16384)
}

func TestTurnCheckpointCorrelatesClaudePromptID(t *testing.T) {
	transcript := `{"type":"user","uuid":"prompt-1","message":{"content":"recorded prompt"}}` + "\n" + `{"type":"assistant","message":{"content":"stale transcript response"}}`
	for _, promptID := range []string{"prompt-1", "missing-prompt"} {
		t.Run(promptID, func(t *testing.T) {
			request := captureTurnRequest(t, "claude", transcript, map[string]any{"prompt_id": promptID, "last_assistant_message": "current response"})
			want := &checkpointTurnInfo{Response: new("current response")}
			if promptID == "prompt-1" {
				want.Prompt = new("recorded prompt")
			}
			require.Equal(t, want, request.Turn)
		})
	}
}

func TestTurnCheckpointDoesNotUseTranscriptResponseWhenHookResponseIsEmpty(t *testing.T) {
	transcript := `{"type":"user","message":{"content":"current prompt"}}` + "\n" + `{"type":"assistant","message":{"content":"stale transcript response"}}`
	request := captureTurnRequest(t, "claude", transcript, map[string]any{"last_assistant_message": ""})
	require.Equal(t, &checkpointTurnInfo{Prompt: new("current prompt")}, request.Turn)
}

func TestTurnCheckpointCanCaptureWithoutTranscriptMetadata(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		for _, tc := range []struct {
			name       string
			transcript string
			fields     map[string]any
		}{
			{"missing", "", map[string]any{"transcript_path": filepath.Join(t.TempDir(), "missing")}},
			{"empty", "", nil},
			{"corrupt", "{\nnot-json\n", nil},
		} {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				fields := map[string]any{"last_assistant_message": "current response"}
				for key, value := range tc.fields {
					fields[key] = value
				}
				request := captureTurnRequest(t, agent, tc.transcript, fields)
				require.Equal(t, &checkpointTurnInfo{Response: new("current response")}, request.Turn)
				require.Len(t, request.EventID, 64)
			})
		}
	}
}

func TestTurnCheckpointSkipsLargeToolRecordsAndContinuesReading(t *testing.T) {
	transcript := `{"type":"user","message":{"content":[{"type":"tool_result","content":"` + strings.Repeat("x", 2<<20) + `"}]}}` + "\n{corrupt\n" + `{"type":"user","message":{"content":"current prompt"}}`
	request := captureTurnRequest(t, "claude", transcript, map[string]any{"last_assistant_message": "current response"})
	require.Equal(t, &checkpointTurnInfo{Prompt: new("current prompt"), Response: new("current response")}, request.Turn)
}

func TestTurnCheckpointDoesNotReuseTextAfterUnreadableRecords(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		for _, tc := range []struct {
			name   string
			record string
		}{
			{"oversized", `{"type":"user","message":{"content":"` + strings.Repeat("x", 2<<20) + `"}}`},
			{"corrupt", `{"type":"user","message":`},
		} {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				previous := `{"type":"user","message":{"content":"previous prompt"}}`
				if agent == "codex" {
					previous = `{"type":"event_msg","payload":{"type":"user_message","message":"previous prompt"}}`
				}
				request := captureTurnRequest(t, agent, previous+"\n"+tc.record, map[string]any{"last_assistant_message": "current response"})
				require.Equal(t, &checkpointTurnInfo{Response: new("current response")}, request.Turn)
			})
		}
	}
}
