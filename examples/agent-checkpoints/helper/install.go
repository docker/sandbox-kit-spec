package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type agentConfig struct {
	agent string
	path  string
	mode  os.FileMode
}

func installUserHooks(home, agent string) error {
	switch agent {
	case "claude", "both":
		return installAgentHooks(agentConfig{"claude", filepath.Join(home, ".claude", "settings.json"), 0o600})
	case "codex":
		return nil
	default:
		return errors.New("agent must be claude, codex, or both")
	}
}

func installSystemHooks(directory, agent string) error {
	switch agent {
	case "codex", "both":
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return err
		}
		if err := installAgentHooks(agentConfig{"codex", filepath.Join(directory, "hooks.json"), 0o644}); err != nil {
			return err
		}
		// Codex discovers system hooks next to an active system config layer.
		config, err := os.OpenFile(filepath.Join(directory, "config.toml"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return config.Close()
	case "claude":
		return nil
	default:
		return errors.New("agent must be claude, codex, or both")
	}
}

func installAgentHooks(config agentConfig) error {
	path := config.path
	info, err := os.Stat(path)
	mode := config.mode
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("hook configuration is not a regular file")
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if _, err := os.Lstat(path); err == nil {
		return errors.New("hook configuration is a dangling symlink")
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	updated, err := addHooks(data, config.agent)
	if err != nil || bytes.Equal(data, updated) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeConfig(path, updated, mode)
}

func addHooks(data []byte, agent string) ([]byte, error) {
	config := map[string]any{}
	if len(data) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&config); err != nil {
			return nil, fmt.Errorf("invalid hook configuration: %w", err)
		}
		if config == nil {
			return nil, errors.New("hook configuration must be a JSON object")
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, errors.New("hook configuration must contain one JSON object")
		}
	}
	hooks, ok := config["hooks"].(map[string]any)
	if !ok {
		if _, exists := config["hooks"]; exists {
			return nil, errors.New("hooks must be a JSON object")
		}
		hooks = map[string]any{}
		config["hooks"] = hooks
	}
	command := installedCommand + " hook " + agent
	for _, event := range []string{"SessionStart", "Stop"} {
		groups, exists := hooks[event]
		entries := []any{}
		if exists {
			var ok bool
			entries, ok = groups.([]any)
			if !ok {
				return nil, fmt.Errorf("%s hooks must be a JSON array", event)
			}
		}
		present, err := containsCommand(entries, command)
		if err != nil {
			return nil, fmt.Errorf("invalid %s hooks: %w", event, err)
		}
		if !present {
			entries = append(entries, map[string]any{"hooks": []any{map[string]any{
				"type": "command", "command": command, "timeout": 45,
			}}})
		}
		hooks[event] = entries
	}
	updated, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(updated, '\n'), nil
}

func containsCommand(entries []any, command string) (bool, error) {
	for _, entry := range entries {
		group, ok := entry.(map[string]any)
		if !ok {
			return false, errors.New("hook group must be an object")
		}
		hooks, ok := group["hooks"].([]any)
		if !ok {
			return false, errors.New("hook group must contain a hooks array")
		}
		for _, value := range hooks {
			hook, ok := value.(map[string]any)
			if !ok {
				return false, errors.New("hook must be an object")
			}
			if hook["type"] == "command" && hook["command"] == command {
				return true, nil
			}
		}
	}
	return false, nil
}

func writeConfig(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".sbx-checkpoint-hooks-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
