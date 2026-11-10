package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	maxTurnText       = 16 << 10
	maxTranscriptLine = 1 << 20
)

type checkpointTurnInfo struct {
	Prompt    *string `json:"prompt,omitempty"`
	Response  *string `json:"response,omitempty"`
	Truncated bool    `json:"truncated"`
}

type turnTranscript struct {
	prompt              *string
	response            *string
	promptTruncated     bool
	responseTruncated   bool
	turnID              string
	promptID            string
	promptSinceBoundary bool
	eventPrompt         bool
}

type transcriptMessage struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Phase   string          `json:"phase"`
	Content json.RawMessage `json:"content"`
}

func (t *turnTranscript) read(input io.Reader, agent string) error {
	reader := bufio.NewReader(input)
	var line []byte
	oversized := false
	for {
		part, prefix, err := reader.ReadLine()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if len(line)+len(part) > maxTranscriptLine {
			oversized = true
		}
		if !oversized {
			line = append(line, part...)
		}
		if prefix {
			continue
		}
		if oversized {
			t.clearText()
		} else {
			if agent == "claude" {
				t.claude(line)
			} else {
				t.codex(line)
			}
		}
		line, oversized = line[:0], false
	}
}

func (t *turnTranscript) claude(line []byte) {
	var entry struct {
		Type             string            `json:"type"`
		PromptID         string            `json:"promptId"`
		IsMeta           bool              `json:"isMeta"`
		IsSidechain      bool              `json:"isSidechain"`
		IsCompactSummary bool              `json:"isCompactSummary"`
		Message          transcriptMessage `json:"message"`
	}
	if json.Unmarshal(line, &entry) != nil {
		t.clearText()
		return
	}
	if entry.IsMeta || entry.IsSidechain || entry.IsCompactSummary {
		return
	}
	switch entry.Type {
	case "user":
		text, toolResult := messageText(entry.Message.Content)
		if !toolResult {
			t.setPrompt(text)
			t.promptID = entry.PromptID
		}
	case "assistant":
		if t.prompt != nil {
			text, _ := messageText(entry.Message.Content)
			if text != "" {
				t.response, t.responseTruncated = boundedTurnText(text)
			}
		}
	}
}

func (t *turnTranscript) codex(line []byte) {
	var entry struct {
		Type    string `json:"type"`
		Payload struct {
			transcriptMessage
			TurnID  string `json:"turn_id"`
			Message string `json:"message"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &entry) != nil {
		t.clearText()
		return
	}
	payload := entry.Payload
	if entry.Type == "turn_context" && payload.TurnID != "" {
		if t.turnID != payload.TurnID && !t.promptSinceBoundary {
			t.setPrompt("")
		}
		t.turnID, t.promptSinceBoundary = payload.TurnID, false
		return
	}
	switch entry.Type {
	case "event_msg":
		switch payload.Type {
		case "task_started", "turn_started":
			t.setPrompt("")
			t.turnID, t.promptSinceBoundary = payload.TurnID, false
		case "user_message":
			t.setPrompt(payload.Message)
			t.eventPrompt = true
		case "agent_message":
			if payload.Phase == "" || payload.Phase == "final_answer" {
				t.response, t.responseTruncated = boundedTurnText(payload.Message)
			}
		}
	case "response_item":
		if payload.Type != "message" {
			return
		}
		text, _ := messageText(payload.Content)
		switch payload.Role {
		case "user":
			if !t.eventPrompt {
				t.setPrompt(text)
			}
		case "assistant":
			if payload.Phase == "" || payload.Phase == "final_answer" {
				t.response, t.responseTruncated = boundedTurnText(text)
			}
		}
	}
}

func (t *turnTranscript) setPrompt(text string) {
	t.clearText()
	t.prompt, t.promptTruncated = boundedTurnText(text)
	t.promptSinceBoundary = true
}

func (t *turnTranscript) clearText() {
	*t = turnTranscript{turnID: t.turnID}
}

func (t *turnTranscript) info(event hookEvent) *checkpointTurnInfo {
	if (event.TurnID != "" && t.turnID != "" && event.TurnID != t.turnID) || (event.PromptID != "" && event.PromptID != t.promptID) {
		t.prompt, t.response = nil, nil
		t.promptTruncated, t.responseTruncated = false, false
	}
	if event.LastAssistantMessage != nil {
		t.response, t.responseTruncated = boundedTurnText(*event.LastAssistantMessage)
	}
	if t.prompt == nil && t.response == nil {
		return nil
	}
	return &checkpointTurnInfo{Prompt: t.prompt, Response: t.response, Truncated: t.promptTruncated || t.responseTruncated}
}

func messageText(content json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text, false
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return "", false
	}
	var texts []string
	for _, block := range blocks {
		switch block.Type {
		case "tool_result":
			return "", true
		case "text", "input_text", "output_text":
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, "\n"), false
}

func boundedTurnText(text string) (*string, bool) {
	if text == "" {
		return nil, false
	}
	if len(text) <= maxTurnText {
		return new(text), false
	}
	end := maxTurnText
	for !utf8.RuneStart(text[end]) {
		end--
	}
	return new(text[:end]), true
}
