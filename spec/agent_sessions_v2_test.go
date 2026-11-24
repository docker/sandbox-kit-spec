package spec

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

const agentSessionsHead = "schemaVersion: \"3\"\nkind: workload\nprovides: [\"claude@2.1.0\"]\ncapabilities:"

// agent-sessions@2: argv verbs decode through their own accessor, a
// verbless entry is refused, and every verb must carry the prompt (resume
// the session id too) — a verb that silently discards the caller's input
// is the failure mode the rule exists for.
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

	// Each accessor reads its own version only: @1's resume and continue
	// carry no prompt, so reading one version as the other would run the
	// wrong tails.
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

	// Running a prompt in an existing session headlessly needs the
	// prompt, so resume and continue are refused for it by name: resume's
	// session id alone does not satisfy the rule.
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

// @1 keeps its shipped reading: resume and continue reopen a session and
// need no prompt, so a Kit published against it still validates.
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

// The two versions describe the same agent's headless verbs and read
// resume and continue differently, so a descriptor states one of them.
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

// Composition holds both versions to one author: an @1 and an @2 from
// different Kits are two answers about the workload's one agent.
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

// Whichever version declares it, the headless list enumerates the same
// sessions as the interactive one, so the two name one command, in one
// descriptor and across composed Kits.
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
