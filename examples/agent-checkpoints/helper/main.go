package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	version            = "0.1.0"
	checkpointEndpoint = "http://gateway.docker.internal:3128/_sbx/api/checkpoints"
	installedCommand   = "/usr/local/bin/sbx-agent-checkpoint"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, checkpointEndpoint); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, input io.Reader, output io.Writer, endpoint string) error {
	if len(args) == 1 && args[0] == "--version" {
		_, err := fmt.Fprintln(output, version)
		return err
	}
	if len(args) != 2 {
		return errors.New("usage: sbx-agent-checkpoint install-user|install-system claude|codex|both, or hook claude|codex")
	}
	switch args[0] {
	case "install-user":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		return installUserHooks(home, args[1])
	case "install-system":
		return installSystemHooks("/etc/codex", args[1])
	case "hook":
		response := map[string]string{}
		if err := captureHook(args[1], input, endpoint); err != nil {
			// A checkpoint failure must not ask the agent to continue and repeat Stop.
			response["systemMessage"] = fmt.Sprintf("Filesystem checkpoint was not saved: %v", err)
		}
		return json.NewEncoder(output).Encode(response)
	default:
		return errors.New("expected install-user, install-system, or hook")
	}
}
