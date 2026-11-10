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
	"time"
)

const maxHookInput = 1 << 20

type hookEvent struct {
	Event          string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	TurnID         string `json:"turn_id"`
	TranscriptPath string `json:"transcript_path"`
}

type checkpointRequest struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id,omitempty"`
	EventID   string `json:"event_id"`
	Kind      string `json:"kind"`
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
	eventID, err := checkpointEventID(agent, data, event)
	if err != nil {
		return err
	}
	request := checkpointRequest{Agent: agent, SessionID: event.SessionID, TurnID: event.TurnID, EventID: eventID, Kind: kind}
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
		return fmt.Errorf("checkpoint request returned HTTP %d", response.StatusCode)
	}
	var checkpoint struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&checkpoint); err != nil || checkpoint.ID == "" {
		return errors.New("checkpoint response has no checkpoint id")
	}
	return nil
}

func checkpointEventID(agent string, data []byte, event hookEvent) (string, error) {
	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, agent+"\x00")
	_, _ = hash.Write(canonical)
	if event.Event == "Stop" && event.TranscriptPath != "" {
		info, err := os.Stat(event.TranscriptPath)
		if err != nil {
			return "", fmt.Errorf("stat turn transcript: %w", err)
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("turn transcript is not a regular file")
		}
		file, err := os.Open(event.TranscriptPath)
		if err != nil {
			return "", fmt.Errorf("open turn transcript: %w", err)
		}
		defer func() { _ = file.Close() }()
		info, err = file.Stat()
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("turn transcript is not a regular file")
		}
		_, _ = hash.Write([]byte{0})
		if _, err := io.Copy(hash, file); err != nil {
			return "", fmt.Errorf("read turn transcript: %w", err)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
