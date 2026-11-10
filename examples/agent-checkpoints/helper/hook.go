package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const maxHookInput = 1 << 20

type hookEvent struct {
	Event                string  `json:"hook_event_name"`
	SessionID            string  `json:"session_id"`
	TurnID               string  `json:"turn_id"`
	PromptID             string  `json:"prompt_id"`
	TranscriptPath       string  `json:"transcript_path"`
	LastAssistantMessage *string `json:"last_assistant_message"`
}

type checkpointRequest struct {
	Agent     string              `json:"agent"`
	SessionID string              `json:"session_id"`
	TurnID    string              `json:"turn_id,omitempty"`
	EventID   string              `json:"event_id"`
	Kind      string              `json:"kind"`
	Turn      *checkpointTurnInfo `json:"turn,omitempty"`
}

func captureHook(agent string, input io.Reader, endpoint string) error {
	if agent != "claude" && agent != "codex" {
		return errors.New("hook agent must be claude or codex")
	}
	data, err := io.ReadAll(io.LimitReader(input, maxHookInput+1))
	if err != nil {
		return err
	}
	if len(data) > maxHookInput {
		return errors.New("hook input exceeds 1 MiB")
	}
	var event hookEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("invalid hook event: %w", err)
	}
	var kind string
	switch event.Event {
	case "SessionStart":
		kind = "baseline"
	case "Stop":
		kind = "turn"
	default:
		return nil
	}
	if event.SessionID == "" {
		return errors.New("hook event has no session_id")
	}
	eventID, turn, err := checkpointEvent(agent, data, event)
	if err != nil {
		return err
	}
	if kind == "turn" && event.TurnID == "" {
		event.TurnID = "event:" + eventID
	}
	request := checkpointRequest{Agent: agent, SessionID: event.SessionID, TurnID: event.TurnID, EventID: eventID, Kind: kind, Turn: turn}
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   40 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("checkpoint request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := fmt.Sprintf("checkpoint request returned HTTP %d", response.StatusCode)
		body, readErr := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
		var detail struct {
			Message string `json:"message"`
		}
		if readErr == nil && len(body) <= 64<<10 && json.Unmarshal(body, &detail) == nil {
			if reason := strings.TrimSpace(detail.Message); reason != "" && len(reason) <= 1024 {
				message += ": " + reason
			}
		}
		return errors.New(message)
	}
	var checkpoint struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&checkpoint); err != nil || checkpoint.ID == "" {
		return errors.New("checkpoint response has no checkpoint id")
	}
	return nil
}

func checkpointEvent(agent string, data []byte, event hookEvent) (string, *checkpointTurnInfo, error) {
	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return "", nil, err
	}
	canonical, err := json.Marshal(fields)
	if err != nil {
		return "", nil, err
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, agent+"\x00")
	_, _ = hash.Write(canonical)
	var transcript turnTranscript
	if event.Event == "Stop" && event.TranscriptPath != "" {
		info, err := os.Stat(event.TranscriptPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", nil, fmt.Errorf("stat turn transcript: %w", err)
		}
		if err == nil && !info.Mode().IsRegular() {
			return "", nil, errors.New("turn transcript is not a regular file")
		}
		if err == nil {
			file, err := os.Open(event.TranscriptPath)
			if err != nil {
				return "", nil, fmt.Errorf("open turn transcript: %w", err)
			}
			defer func() { _ = file.Close() }()
			info, err = file.Stat()
			if err != nil {
				return "", nil, err
			}
			if !info.Mode().IsRegular() {
				return "", nil, errors.New("turn transcript is not a regular file")
			}
			_, _ = hash.Write([]byte{0})
			if err := transcript.read(io.TeeReader(file, hash), agent); err != nil {
				return "", nil, fmt.Errorf("read turn transcript: %w", err)
			}
		}
	}
	var turn *checkpointTurnInfo
	if event.Event == "Stop" {
		turn = transcript.info(event)
	}
	return hex.EncodeToString(hash.Sum(nil)), turn, nil
}
