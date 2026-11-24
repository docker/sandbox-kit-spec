package spec

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

const agentSessionsHead = "schemaVersion: \"3\"\nkind: workload\nprovides: [\"claude@2.1.0\"]\ncapabilities:"

// agent-sessions@2: every verb must carry the prompt, resume the session
// id too.
func TestAgentSessionsV2Need(t *testing.T) {
	base := `# syntax=docker/sandbox-kit:3
schemaVersion: "3"
kind: workload
provides: ["claude@2.1.0"]
capabilities:
  - type: com.docker.sandbox/agent-sessions@2
    config:
      prompt: ["-p", "{{.Prompt}}"]
      resume: ["-p", "--resume", "{{.SessionID}}", "{{.Prompt}}"]
      continue: ["-p", "--continue", "{{.Prompt}}"]
      list: [sh, -c, "ls sessions"]
`
	d := decodeValid(t, base)
	sessions, err := AgentSessionsV2Of(d.Capabilities)
	require.NoError(t, err)
	require.Equal(t, []string{"-p", "{{.Prompt}}"}, sessions.Prompt)
	require.Equal(t, []string{"-p", "--resume", "{{.SessionID}}", "{{.Prompt}}"}, sessions.Resume)
	require.Equal(t, []string{"-p", "--continue", "{{.Prompt}}"}, sessions.Continue)
	require.Equal(t, CommandLine{"sh", "-c", "ls sessions"}, sessions.List)

	v1, err := AgentSessionsOf(d.Capabilities)
	require.NoError(t, err)
	require.Nil(t, v1)

	cases := map[string]string{
		"no verbs": `
  - type: com.docker.sandbox/agent-sessions@2
    config: {}
`,
		"prompt without placeholder": `
  - type: com.docker.sandbox/agent-sessions@2
    config:
      prompt: ["-p"]
`,
		"resume without session id": `
  - type: com.docker.sandbox/agent-sessions@2
    config:
      resume: ["-p", "--resume", "{{.Prompt}}"]
`,
		"unknown config key": `
  - type: com.docker.sandbox/agent-sessions@2
    config:
      prompts: ["-p", "{{.Prompt}}"]
`,
		"second declaration": `
  - type: com.docker.sandbox/agent-sessions@2
    config:
      prompt: ["-p", "{{.Prompt}}"]
  - type: com.docker.sandbox/agent-sessions@2
    config:
      continue: ["--continue", "{{.Prompt}}"]
`,
	}
	for name, needs := range cases {
		t.Run(name, func(t *testing.T) {
			d, err := Decode([]byte(agentSessionsHead + needs))
			require.NoError(t, err)
			_, err = Validate(d)
			require.Error(t, err)
		})
	}

	for verb, tail := range map[string]string{
		"resume":   `["--resume", "{{.SessionID}}"]`,
		"continue": `["--continue"]`,
	} {
		t.Run(verb+" without prompt", func(t *testing.T) {
			d, err := Decode([]byte(agentSessionsHead + "\n  - type: com.docker.sandbox/agent-sessions@2\n    config:\n      " + verb + ": " + tail + "\n"))
			require.NoError(t, err)
			_, err = Validate(d)
			require.ErrorContains(t, err, verb+" must reference "+SessionPromptPlaceholder)
		})
	}

	// A placeholder inside a larger token still counts.
	d, err = Decode([]byte(agentSessionsHead + `
  - type: com.docker.sandbox/agent-sessions@2
    config:
      continue: ["--continue", "--print={{.Prompt}}"]
`))
	require.NoError(t, err)
	_, err = Validate(d)
	require.NoError(t, err)
}

// @1's resume and continue still need no prompt.
func TestAgentSessionsV1KeepsPromptlessReopen(t *testing.T) {
	d, err := Decode([]byte(agentSessionsHead + `
  - type: com.docker.sandbox/agent-sessions@1
    config:
      resume: ["--resume", "{{.SessionID}}"]
      continue: ["--continue"]
`))
	require.NoError(t, err)
	_, err = Validate(d)
	require.NoError(t, err)
	v2, err := AgentSessionsV2Of(d.Capabilities)
	require.NoError(t, err)
	require.Nil(t, v2)
}

// A descriptor states one agent-sessions version.
func TestAgentSessionsVersionsExclusive(t *testing.T) {
	d, err := Decode([]byte(agentSessionsHead + `
  - type: com.docker.sandbox/agent-sessions@1
    config:
      continue: ["--continue"]
  - type: com.docker.sandbox/agent-sessions@2
    config:
      continue: ["-p", "--continue", "{{.Prompt}}"]
`))
	require.NoError(t, err)
	_, err = Validate(d)
	require.ErrorContains(t, err, "capabilities[1].type")
	require.ErrorContains(t, err, "a descriptor states one agent-sessions version")
}

// An @1 and an @2 from different Kits are two authors.
func TestMergeHoldsAgentSessionsVersionsToOneAuthor(t *testing.T) {
	sessions := `schemaVersion: "3"
kind: %s
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/agent-sessions@%s
    config: {continue: %s}
`
	_, err := Merge([]Contribution{
		contribute(t, "a", fmt.Sprintf(sessions, KindWorkload, "1", "[--continue]")),
		contribute(t, "b", fmt.Sprintf(sessions, KindMixin, "2", `[-p, --continue, "{{.Prompt}}"]`)),
	}, MergeOptions{})
	require.ErrorContains(t, err, "one author")

	out := mergeOK(t,
		contribute(t, "a", fmt.Sprintf(sessions, KindWorkload, "2", `[-p, --continue, "{{.Prompt}}"]`)),
		contribute(t, "b", fmt.Sprintf(sessions, KindMixin, "2", `[-p, --continue, "{{.Prompt}}"]`)),
	).Descriptor
	require.Len(t, out.Capabilities, 1, "an identical restatement is the same ask")
	v2, err := AgentSessionsV2Of(out.Capabilities)
	require.NoError(t, err)
	require.Equal(t, []string{"-p", "--continue", "{{.Prompt}}"}, v2.Continue)
}

// @2's list must match the interactive one, as @1's does.
func TestAgentSessionsV2ListMatchesInteractive(t *testing.T) {
	d, err := Decode([]byte(agentSessionsHead + `
  - type: com.docker.sandbox/agent-sessions@2
    config: {list: [ls, ids]}
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config: {list: [ls, all]}
`))
	require.NoError(t, err)
	_, err = Validate(d)
	require.ErrorContains(t, err, "differs from agent-sessions list")
	require.ErrorContains(t, err, "capabilities[1].config.list")

	headless := contribute(t, "headless", `schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/agent-sessions@2
    config: {list: [ls, ids]}
`)
	interactive := func(list string) Contribution {
		return contribute(t, "tui", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config: {list: `+list+`}
`)
	}
	_, err = Compose([]Contribution{headless, interactive(`[ls, ids]`)})
	require.NoError(t, err)
	_, err = Compose([]Contribution{headless, interactive(`[ls, all]`)})
	require.ErrorContains(t, err, "differs from agent-interactive-sessions list")
}
