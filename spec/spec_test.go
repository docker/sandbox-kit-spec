package spec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Create-phase args may parameterize capability configs. The authored and
// published forms validate leniently (typed checks defer for parameterized
// entries); ExpandCreateArgs produces the effective descriptor, which
// validates strictly — that is the form enforcement, the lock, and the
// gate judge.
func TestParameterizedCapabilities(t *testing.T) {
	const y = `schemaVersion: "3"
kind: mixin
version: "1.0.0"
args:
  registry:
    default: registry.example.com
    pattern: '^[a-z0-9.-]+$'
  port:
    default: "8080"
    pattern: '^[0-9]+$'
  dir:
    default: /project
    pattern: '^/[A-Za-z0-9._/-]+$'
capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow:
          - ${{ kit.args.registry }}
          - api.github.com
  - type: com.docker.sandbox/port@1
    config:
      container: ${{ kit.args.port }}
  - type: com.docker.sandbox/volume@1
    config: {path: "${{ kit.args.dir }}/cache"}
  - type: com.docker.sandbox/credential@1
    optional: true
    config:
      service: github
      phase: runtime
      apiKey:
        name: GH_TOKEN
        inject:
          - {domain: api.github.com, header: Authorization, format: "Bearer %s"}
`
	raw := []byte(y)
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidateRaw(raw, d)
	require.NoError(t, err, "parameterized entries defer typed validation")
	_, err = ValidatePublished(raw, d)
	require.NoError(t, err)

	values, err := ResolveArgs(d.Args, map[string]string{"port": "3000"})
	require.NoError(t, err)
	eff, err := ExpandCreateArgs(raw, d.Args, values)
	require.NoError(t, err)
	de, err := Decode(eff)
	require.NoError(t, err)
	_, err = ValidateEffective(eff, de)
	require.NoError(t, err)

	ports, err := PortsOf(de.Capabilities)
	require.NoError(t, err)
	require.Equal(t, 3000, ports[0].Container, "textual expansion lands in typed fields")
	vols, err := VolumesOf(de.Capabilities)
	require.NoError(t, err)
	require.Equal(t, "/project/cache", vols[0].Path)
	policy, err := NetworkPolicyOf(de.Capabilities)
	require.NoError(t, err)
	require.Contains(t, policy.Runtime.Allow, "registry.example.com")

	t.Run("effective form is strictly validated", func(t *testing.T) {
		bad, err := ExpandCreateArgs(raw, d.Args, map[string]string{"registry": "registry.example.com", "port": "99999", "dir": "/project"})
		require.NoError(t, err)
		db, err := Decode(bad)
		require.NoError(t, err)
		_, err = ValidateEffective(bad, db)
		require.ErrorContains(t, err, "out of range", "deferred checks run on the effective form")
	})

	t.Run("unresolved reference fails expansion", func(t *testing.T) {
		_, err := ExpandCreateArgs(raw, d.Args, map[string]string{"port": "1"})
		require.ErrorContains(t, err, "no resolved value")
	})

	t.Run("leftover references fail effective validation", func(t *testing.T) {
		_, err := ValidateEffective(raw, d)
		require.ErrorContains(t, err, "still references kit args")
	})
}

func TestParameterizedNetworkPolicyV2DefersValueChecks(t *testing.T) {
	for _, test := range []struct{ name, entry, host, method, path, want string }{
		{"bare host", `"${{ kit.args.host }}"`, "*.example.com", "GET", "/v1/**", ""},
		{"hosts only", `{hosts: ["${{ kit.args.host }}"]}`, "*.example.com", "GET", "/v1/**", ""},
		{"bounded entry", `{hosts: ["${{ kit.args.host }}"], methods: ["${{ kit.args.method }}"], paths: ["${{ kit.args.path }}"]}`, "api.example.com", "GET", "/v1/**", ""},
		{"invalid method", `{hosts: ["${{ kit.args.host }}"], methods: ["${{ kit.args.method }}"]}`, "api.example.com", "get", "/v1/**", "is not an uppercase HTTP method"},
		{"invalid path", `{hosts: ["${{ kit.args.host }}"], methods: [GET], paths: ["${{ kit.args.path }}"]}`, "api.example.com", "GET", "v1/**", `must start with "/"`},
		{"bounded wildcard", `{hosts: ["${{ kit.args.host }}"], methods: [GET]}`, "*.example.com", "GET", "/v1/**", "bounds the pattern"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(`schemaVersion: "3"
kind: mixin
args:
  host: {required: true}
  method: {required: true}
  path: {required: true}
capabilities:
  - type: com.docker.sandbox/network-policy@2
    config:
      runtime:
        allow: [` + test.entry + `]
`)
			d, err := Decode(raw)
			require.NoError(t, err)
			_, err = ValidateRaw(raw, d)
			require.NoError(t, err)
			_, err = ValidatePublished(raw, d)
			require.NoError(t, err)

			values, err := ResolveArgs(d.Args, map[string]string{"host": test.host, "method": test.method, "path": test.path})
			require.NoError(t, err)
			effective, err := ExpandCreateArgs(raw, d.Args, values)
			require.NoError(t, err)
			d, err = Decode(effective)
			require.NoError(t, err)
			_, err = ValidateEffective(effective, d)
			if test.want != "" {
				require.ErrorContains(t, err, test.want)
				return
			}
			require.NoError(t, err)
			policy, err := NetworkPolicyV2Of(d.Capabilities)
			require.NoError(t, err)
			require.Len(t, policy.Runtime.Allow, 1)
			require.Equal(t, []string{test.host}, policy.Runtime.Allow[0].Hosts)
		})
	}
}

// OCIAnnotations projects the descriptor's display metadata onto the
// standard org.opencontainers.image.* keys: empty fields emit nothing,
// version comes from version: or a unanimous provides version, and
// disagreement yields no version at all.
func TestOCIAnnotations(t *testing.T) {
	d := &Descriptor{
		DisplayName: "GitHub CLI",
		Description: "gh from nixpkgs",
		Author:      "Christian Dupuis <cd@docker.com>",
		SourceURL:   "https://github.com/cli/cli",
		Licenses:    []string{"MIT", "Apache-2.0"},
		Provides:    []string{"gh@2.98.0"},
	}
	a := OCIAnnotations(d)
	require.Equal(t, "GitHub CLI", a[OCIAnnotationTitle])
	require.Equal(t, "gh from nixpkgs", a[OCIAnnotationDescription])
	require.Equal(t, "Christian Dupuis <cd@docker.com>", a[OCIAnnotationAuthors])
	require.Equal(t, "https://github.com/cli/cli", a[OCIAnnotationSource])
	require.Equal(t, "MIT,Apache-2.0", a[OCIAnnotationLicenses])
	require.Equal(t, "2.98.0", a[OCIAnnotationVersion], "a unanimous provides version is the kit's version")

	require.Empty(t, OCIAnnotations(&Descriptor{}), "empty fields emit no keys")

	versioned := OCIAnnotations(&Descriptor{Version: "9.9.9", Provides: []string{"gh@2.98.0"}})
	require.Equal(t, "9.9.9", versioned[OCIAnnotationVersion], "version: outranks provides")

	disagreeing := OCIAnnotations(&Descriptor{Provides: []string{"a@1.0.0", "b@2.0.0"}})
	require.NotContains(t, disagreeing, OCIAnnotationVersion, "an ambiguous version is worse than none")
}

// The build stamp is written by one frontend and read by anything that
// pulls the kit, so the pair has to round-trip, and the encoding has to
// be the compact JSON the manifest carries.
func TestBuiltByRoundTrips(t *testing.T) {
	want := BuiltBy{
		Name:     "docker/sandbox-kit",
		Version:  "3.0.0-m.5",
		Revision: "2f9a1c4e8b7d6a5c4b3e2d1f0a9b8c7d6e5f4a3b",
	}
	raw, err := want.Marshal()
	require.NoError(t, err)
	require.Equal(t,
		`{"name":"docker/sandbox-kit","version":"3.0.0-m.5","revision":"2f9a1c4e8b7d6a5c4b3e2d1f0a9b8c7d6e5f4a3b"}`,
		raw, "compact, and byte-deterministic for a given build")

	got, present, err := ParseBuiltBy(map[string]string{AnnotationBuiltBy: raw})
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, want, got)
}

// A build that recorded no commit omits the key rather than carrying an
// empty one, so a reader can tell "not recorded" from "recorded as
// nothing".
func TestBuiltByOmitsAnAbsentRevision(t *testing.T) {
	raw, err := BuiltBy{Name: "docker/sandbox-kit", Version: "dev"}.Marshal()
	require.NoError(t, err)
	require.Equal(t, `{"name":"docker/sandbox-kit","version":"dev"}`, raw)
}

// Absence is the ordinary case for a kit published before the annotation
// existed, and it is not an error; a value that will not decode is.
func TestParseBuiltBySeparatesAbsenceFromMalformed(t *testing.T) {
	_, present, err := ParseBuiltBy(map[string]string{})
	require.NoError(t, err)
	require.False(t, present)

	_, present, err = ParseBuiltBy(map[string]string{AnnotationBuiltBy: "sandbox-kit 3.0.0"})
	require.True(t, present, "a value that is there but unreadable is still there")
	require.Error(t, err)
}

func TestCapabilityTypes(t *testing.T) {
	require.Empty(t, CapabilityTypes(nil))

	caps := []Capability{
		{Type: CapabilityNetworkPolicy},
		{Type: CapabilityCredential, Config: map[string]any{"service": "github"}},
		{Type: CapabilityCredential, Config: map[string]any{"service": "anthropic"}},
		{Type: CapabilityVolume},
	}
	require.Equal(t,
		"com.docker.sandbox/credential@1,com.docker.sandbox/network-policy@1,com.docker.sandbox/volume@1",
		CapabilityTypes(caps),
		"deduplicated and sorted")

	d, err := Decode([]byte(claudeYAML))
	require.NoError(t, err)
	require.Equal(t,
		"com.docker.sandbox/agent-context@1,com.docker.sandbox/credential@1,com.docker.sandbox/lifecycle@1,com.docker.sandbox/network-policy@1,com.docker.sandbox/volume@1",
		CapabilityTypes(d.Capabilities))
}

// TestPublishedJSONRoundTrips pins the annotation contract: the frontend
// publishes json.Marshal of the decoded descriptor, and consumers decode
// it with the same strict YAML decoder as authored files (YAML accepts
// JSON). Any json/yaml tag divergence on a spec type breaks this.
func TestPublishedJSONRoundTrips(t *testing.T) {
	for name, src := range map[string]string{"gh": ghYAML, "claude": claudeYAML} {
		t.Run(name, func(t *testing.T) {
			d, err := Decode([]byte(src))
			require.NoError(t, err)

			published, err := json.Marshal(d)
			require.NoError(t, err)

			rt, err := Decode(published)
			require.NoError(t, err)
			require.Equal(t, d, rt)

			_, err = ValidateRaw(published, rt)
			require.NoError(t, err)
		})
	}
}

// ghYAML is the gh mixin from the design appendix: buildArg-phase version
// arg, runtime-only network and credentials, mixin guidance.
const ghYAML = `# syntax=docker/sandbox-kit:3
schemaVersion: "3"
displayName: GitHub CLI
description: gh, installed from the official release tarball
sourceUrl: https://github.com/cli/cli
licenses: [MIT]

kind: mixin

args:
  version:
    default: "2.98.0"
    pattern: '^[0-9]+\.[0-9]+\.[0-9]+$'
    description: GitHub CLI release to install
    buildArg: GH_VERSION

provides: ["gh@${{ kit.args.version }}"]

capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow:
          - github.com
          - api.github.com
          - uploads.github.com
  - type: com.docker.sandbox/credential@1
    description: GitHub API access for gh
    optional: true
    config:
      service: github
      phase: runtime
      apiKey:
        name: GH_TOKEN
        proxyManaged: true
        inject:
          - {domain: api.github.com, header: Authorization, format: "Bearer %s"}
          - {domain: uploads.github.com, header: Authorization, format: "Bearer %s"}
          - {domain: github.com, header: Authorization, format: "Bearer %s"}

  - type: com.docker.sandbox/agent-context@1
    config:
      contentFile: ./gh-context.md
`

// claudeYAML is the sandbox kit from the design appendix.
const claudeYAML = `# syntax=docker/sandbox-kit:3
schemaVersion: "3"
displayName: Claude Code
description: Anthropic's Claude Code agent
sourceUrl: https://github.com/anthropics/claude-code
licenses: [Apache-2.0]

kind: workload
provides: [claude@2.1.0]

capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow:
          - api.anthropic.com:443
          - platform.claude.com:443
          - claude.ai:443
          - console.anthropic.com:443
          - statsig.anthropic.com:443
  - type: com.docker.sandbox/credential@1
    description: Anthropic API access
    config:
      service: anthropic
      phase: runtime
      apiKey:
        name: ANTHROPIC_API_KEY
        proxyManaged: true
        inject:
          - {domain: api.anthropic.com, header: x-api-key, format: "%s"}
          - {domain: console.anthropic.com, header: x-api-key, format: "%s"}
          - {domain: claude.ai, header: x-api-key, format: "%s"}
      oauth:
        tokenEndpoint: {host: platform.claude.com, path: /v1/oauth/token}
        resourceHosts: [api.anthropic.com]
        sentinels:
          accessToken: sk-ant-oat01-proxy-managed
          refreshToken: sk-ant-ort01-proxy-managed
        credentialFile:
          path: ~/.claude/.credentials.json
          structure:
            claudeAiOauth: {accessToken: "{{.AccessToken}}"}
        responseFields: {accessToken: access_token, expiresIn: expires_in}
        passthrough: false
  - type: com.docker.sandbox/volume@1
    config: {path: /home/agent/.claude/projects, size: 2g}
  - type: com.docker.sandbox/lifecycle@1
    config:
      install:
        - command:
            - sh
            - -c
            - |
              set -e
              ws="${WORKSPACE_DIR:-/}"
              printf '%s' "$ws" > /home/agent/.claude.json
          user: "0"
          env: [WORKSPACE_DIR]
          description: Seed trust flags against the resolved workspace
      startup:
        - command: [sh, -c, "chown -R agent:agent /home/agent/.claude/projects"]
          user: "0"
          description: Re-own the volume mount root
  - type: com.docker.sandbox/agent-context@1
    config:
      filename: CLAUDE.md
      contentFile: ./CLAUDE-context.md
`

// dockerfile: names the companion explicitly; it shares exactly-one-home
// semantics with build:, and its path must stay inside the descriptor's
// directory — the dockerfile context's root, beyond which nothing can be
// read anyway.
func TestValidateRecipe(t *testing.T) {
	base := "schemaVersion: \"3\"\nkind: mixin\nprovides: [\"x@1.0.0\"]\n"

	d, err := Decode([]byte(base + "dockerfile: ./recipes/x.dockerfile\n"))
	require.NoError(t, err)
	_, err = Validate(d)
	require.NoError(t, err)

	cases := map[string]string{
		"both homes":    "dockerfile: ./x.dockerfile\nbuild: |\n  FROM scratch\n",
		"absolute path": "dockerfile: /etc/x.dockerfile\n",
		"escapes dir":   "dockerfile: ../shared/x.dockerfile\n",
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			d, err := Decode([]byte(base + extra))
			require.NoError(t, err)
			_, err = Validate(d)
			require.Error(t, err)
		})
	}
}

// The agent-sessions declaration: argv verbs decode through the
// accessor, a verbless entry is refused, and the prompt/resume verbs
// must carry their placeholder — a verb that silently discards the
// caller's input is the failure mode the rule exists for.
func TestAgentSessionsNeed(t *testing.T) {
	base := `# syntax=docker/sandbox-kit:3
schemaVersion: "3"
kind: workload
provides: ["claude@2.1.0"]
capabilities:
  - type: com.docker.sandbox/agent-sessions@1
    config:
      prompt: ["-p", "{{.Prompt}}"]
      resume: ["--resume", "{{.SessionID}}"]
      continue: ["--continue"]
      list: [sh, -c, "ls sessions"]
`
	d := decodeValid(t, base)
	sessions, err := AgentSessionsOf(d.Capabilities)
	require.NoError(t, err)
	require.Equal(t, []string{"-p", "{{.Prompt}}"}, sessions.Prompt)
	require.Equal(t, []string{"--resume", "{{.SessionID}}"}, sessions.Resume)
	require.Equal(t, []string{"--continue"}, sessions.Continue)
	require.Equal(t, CommandLine{"sh", "-c", "ls sessions"}, sessions.List)

	cases := map[string]string{
		"no verbs": `
  - type: com.docker.sandbox/agent-sessions@1
    config: {}
`,
		"prompt without placeholder": `
  - type: com.docker.sandbox/agent-sessions@1
    config:
      prompt: ["-p"]
`,
		"null prompt beside a valid verb": `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      prompt: null
      continue: [--continue]
`,
		"null newSession beside a valid verb": `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      newSession: null
      continue: [--continue]
`,
		"null element in a tail": `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      continue: [--continue, null]
`,
		"null list beside a valid verb": `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      list: null
      continue: [--continue]
`,
		"resume without placeholder": `
  - type: com.docker.sandbox/agent-sessions@1
    config:
      resume: ["--resume"]
`,
		"unknown config key": `
  - type: com.docker.sandbox/agent-sessions@1
    config:
      prompts: ["-p", "{{.Prompt}}"]
`,
		"second declaration": `
  - type: com.docker.sandbox/agent-sessions@1
    config:
      prompt: ["-p", "{{.Prompt}}"]
  - type: com.docker.sandbox/agent-sessions@1
    config:
      continue: ["--continue"]
`,
	}
	for name, needs := range cases {
		t.Run(name, func(t *testing.T) {
			y := "schemaVersion: \"3\"\nkind: workload\nprovides: [\"claude@2.1.0\"]\ncapabilities:" + needs
			d, err := Decode([]byte(y))
			require.NoError(t, err)
			_, err = Validate(d)
			require.Error(t, err)
		})
	}

	// A placeholder inside a larger token still counts.
	embedded := `schemaVersion: "3"
kind: workload
provides: ["claude@2.1.0"]
capabilities:
  - type: com.docker.sandbox/agent-sessions@1
    config:
      prompt: ["--print={{.Prompt}}"]
`
	d2, err := Decode([]byte(embedded))
	require.NoError(t, err)
	_, err = Validate(d2)
	require.NoError(t, err)
}

func TestAgentInteractiveSessionsNeed(t *testing.T) {
	const head = "schemaVersion: \"3\"\nkind: workload\nprovides: [\"claude@2.1.0\"]\ncapabilities:"
	base := `# syntax=docker/sandbox-kit:3
schemaVersion: "3"
kind: workload
provides: ["claude@2.1.0"]
capabilities:
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      prompt: ["{{.Prompt}}"]
      resume: ["--resume", "{{.SessionID}}"]
      continue: ["--continue"]
      newSession: []
      sessionPicker: ["--resume"]
      list: [sh, -c, "ls sessions"]
`
	d := decodeValid(t, base)
	sessions, err := AgentInteractiveSessionsOf(d.Capabilities)
	require.NoError(t, err)
	require.Equal(t, []string{"{{.Prompt}}"}, sessions.Prompt)
	require.Equal(t, []string{"--resume", "{{.SessionID}}"}, sessions.Resume)
	require.Equal(t, []string{"--continue"}, sessions.Continue)
	require.Equal(t, []string{"--resume"}, sessions.SessionPicker)
	require.Equal(t, CommandLine{"sh", "-c", "ls sessions"}, sessions.List)

	// Presence is the verb: an authored empty tail decodes non-nil, an
	// absent one nil, so "launch argv alone" and "unsupported" stay apart.
	require.NotNil(t, sessions.NewSession)
	require.Empty(t, sessions.NewSession)
	bare := decodeValid(t, head+`
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      newSession: []
`)
	only, err := AgentInteractiveSessionsOf(bare.Capabilities)
	require.NoError(t, err)
	require.NotNil(t, only.NewSession, "an empty newSession is a declared verb")
	require.Empty(t, only.NewSession)
	require.Nil(t, only.Prompt)
	require.Nil(t, only.Resume)
	require.Nil(t, only.Continue)
	require.Nil(t, only.SessionPicker)

	// The same distinction holds for a config built in code, the way JSON
	// input and programmatic callers reach the accessor.
	fromJSON := []Capability{{
		Type:   CapabilityAgentInteractiveSessions,
		Config: map[string]any{"newSession": []any{}, "prompt": []any{"{{.Prompt}}"}},
	}}
	got, err := AgentInteractiveSessionsOf(fromJSON)
	require.NoError(t, err)
	require.NotNil(t, got.NewSession)
	require.Empty(t, got.NewSession)
	require.Nil(t, got.SessionPicker)

	cases := map[string]string{
		"no verbs": `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config: {}
`,
		"prompt without placeholder": `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      prompt: ["--prompt"]
`,
		"empty prompt cannot carry the placeholder": `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      prompt: []
`,
		"resume without placeholder": `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      resume: ["--resume"]
`,
		"empty resume cannot carry the placeholder": `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      resume: []
`,
		"unknown config key": `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      new_session: []
`,
		"empty list command alone": `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      list: []
`,
		"empty list command beside a verb": `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      continue: [--continue]
      list: []
`,
		"empty string list command beside a verb": `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      continue: [--continue]
      list: ""
`,
		"blank executable in a list command": `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      continue: [--continue]
      list: [""]
`,
		"blank string list command beside a verb": `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      continue: [--continue]
      list: "  "
`,
		"second declaration": `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      newSession: []
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      continue: ["--continue"]
`,
	}
	for name, needs := range cases {
		t.Run(name, func(t *testing.T) {
			d, err := Decode([]byte(head + needs))
			require.NoError(t, err)
			_, err = Validate(d)
			require.Error(t, err)
		})
	}

	// A placeholder inside a larger token still counts, and an empty
	// continue or picker is a verb like any other tail.
	embedded := head + `
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config:
      prompt: ["--prompt={{.Prompt}}"]
      continue: []
      sessionPicker: []
      list: "claude-sessions --format ids"
`
	d2, err := Decode([]byte(embedded))
	require.NoError(t, err)
	_, err = Validate(d2)
	require.NoError(t, err)
}

// newSession and lifecycle's interactive tail name the same launch, so a
// Kit that states both states one argv. Absence on either side is not a
// disagreement: newSession omitted falls back to the lifecycle tail.
func TestAgentInteractiveSessionsAgreeWithLifecycle(t *testing.T) {
	const head = "schemaVersion: \"3\"\nkind: workload\nprovides: [\"claude@2.1.0\"]\ncapabilities:"
	sessions := func(config string) string {
		return "\n  - type: com.docker.sandbox/agent-interactive-sessions@1\n    config: {" + config + "}"
	}
	lifecycle := func(config string) string {
		return "\n  - type: com.docker.sandbox/lifecycle@1\n    config: {" + config + "}"
	}
	cases := []struct {
		name  string
		needs string
		ok    bool
	}{
		{"both empty", sessions("newSession: []") + lifecycle("interactive: [], startup: [{command: echo ready}]"), true},
		{"newSession empty, lifecycle absent", sessions("newSession: []") + lifecycle("startup: [{command: echo ready}]"), true},
		{"same tail", sessions("newSession: [--tui]") + lifecycle("interactive: [--tui]"), true},
		{"newSession absent, lifecycle tail", sessions("continue: [--continue]") + lifecycle("interactive: [--tui]"), true},
		{"newSession tail, no lifecycle", sessions("newSession: [--tui]"), true},
		{"newSession tail, lifecycle without tail", sessions("newSession: [--tui]") + lifecycle("startup: [{command: echo ready}]"), true},
		{"newSession empty, lifecycle tail", sessions("newSession: []") + lifecycle("interactive: [--tui]"), false},
		{"different tails", sessions("newSession: [--tui]") + lifecycle("interactive: [--other]"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Decode([]byte(head + tc.needs))
			require.NoError(t, err)
			_, err = Validate(d)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "disagrees with lifecycle interactive")
		})
	}
}

// list names the same sessions whichever mode opened them, so a Kit that
// declares it on both session capabilities states one command. The two
// spellings of one command decode to the same argv and are equal.
func TestAgentInteractiveSessionsListMatchesAgentSessions(t *testing.T) {
	const head = "schemaVersion: \"3\"\nkind: workload\nprovides: [\"claude@2.1.0\"]\ncapabilities:"
	headless := func(config string) string {
		return "\n  - type: com.docker.sandbox/agent-sessions@1\n    config: {" + config + "}"
	}
	interactive := func(config string) string {
		return "\n  - type: com.docker.sandbox/agent-interactive-sessions@1\n    config: {" + config + "}"
	}
	cases := []struct {
		name  string
		needs string
		ok    bool
	}{
		{"same list", headless(`list: [ls, ids]`) + interactive(`list: [ls, ids]`), true},
		{"string and list spellings of one command", headless(`list: "a b"`) + interactive(`list: [sh, -c, "a b"]`), true},
		{"both strings", headless(`list: "a b"`) + interactive(`list: "a b"`), true},
		{"list on the headless side only", headless(`list: [ls, ids]`) + interactive(`continue: [--continue]`), true},
		{"list on the interactive side only", headless(`continue: [--continue]`) + interactive(`list: [ls, ids]`), true},
		{"different lists", headless(`list: [ls, ids]`) + interactive(`list: [ls, all]`), false},
		{"different strings", headless(`list: "a b"`) + interactive(`list: "a c"`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Decode([]byte(head + tc.needs))
			require.NoError(t, err)
			_, err = Validate(d)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "differs from agent-sessions list")
			require.ErrorContains(t, err, "capabilities[1].config.list")
		})
	}
}

func decodeValid(t *testing.T, y string) *Descriptor {
	t.Helper()
	d, err := Decode([]byte(y))
	require.NoError(t, err)
	warnings, err := ValidateRaw([]byte(y), d)
	require.NoError(t, err)
	require.Empty(t, warnings)
	return d
}

func TestDecodeGhMixin(t *testing.T) {
	d := decodeValid(t, ghYAML)

	require.Equal(t, KindMixin, d.Kind)
	require.Equal(t, "GitHub CLI", d.DisplayName)
	require.Equal(t, []string{"gh@${{ kit.args.version }}"}, d.Provides) //nolint:staticcheck // literal fixture

	require.Len(t, d.Args, 1)
	require.Equal(t, "GH_VERSION", d.Args["version"].BuildArg)
	require.NotNil(t, d.Args["version"].Default)
	require.Equal(t, "2.98.0", *d.Args["version"].Default)

	creds, err := CredentialsOfPhase(d.Capabilities, "runtime")
	require.NoError(t, err)
	require.Len(t, creds, 1)
	require.Equal(t, "github", creds[0].Service)
	require.False(t, creds[0].Required, "optional: true maps to a non-required credential")
	require.Len(t, creds[0].APIKey.Inject, 3)

	agentContext, err := AgentContextOf(d.Capabilities)
	require.NoError(t, err)
	require.NotNil(t, agentContext)
	require.Empty(t, agentContext.Filename)
}

func TestDecodeClaudeSandbox(t *testing.T) {
	d := decodeValid(t, claudeYAML)

	require.Equal(t, KindWorkload, d.Kind)
	lifecycle, err := LifecycleOf(d.Capabilities)
	require.NoError(t, err)
	require.Len(t, lifecycle.Install, 1)
	// String-or-list command flexibility: this one is a 3-element list.
	require.Len(t, lifecycle.Install[0].Command, 3)
	require.Equal(t, []string{"WORKSPACE_DIR"}, lifecycle.Install[0].Env)
	// $VAR inside the hook body is not an arg reference and passes through.
	require.Contains(t, lifecycle.Install[0].Command[2], "${WORKSPACE_DIR:-/}")

	volumes, err := VolumesOf(d.Capabilities)
	require.NoError(t, err)
	require.Len(t, volumes, 1)
	require.Equal(t, "/home/agent/.claude/projects", volumes[0].Path)

	creds, err := CredentialsOfPhase(d.Capabilities, "runtime")
	require.NoError(t, err)
	cred := creds[0]
	require.True(t, cred.Required, "no optional: flag means required")
	require.NotNil(t, cred.OAuth)
	require.Equal(t, "platform.claude.com", cred.OAuth.TokenEndpoint.Host)
	agentContext, err := AgentContextOf(d.Capabilities)
	require.NoError(t, err)
	require.Equal(t, "CLAUDE.md", agentContext.Filename)
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	_, err := Decode([]byte("schemaVersion: \"3\"\nkind: mixin\nname: nope\n"))
	require.ErrorContains(t, err, "name")
}

func TestDecodeRejectsWrongSchemaVersion(t *testing.T) {
	_, err := Decode([]byte("schemaVersion: \"2\"\nkind: mixin\n"))
	require.ErrorContains(t, err, "unsupported schemaVersion")
}

func TestCommandLineStringForm(t *testing.T) {
	d, err := Decode([]byte(`schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      install:
        - command: "pip install my-tool"
          env: [HTTP_PROXY]
`))
	require.NoError(t, err)
	lifecycle, err := LifecycleOf(d.Capabilities)
	require.NoError(t, err)
	require.Equal(t, CommandLine{"sh", "-c", "pip install my-tool"}, lifecycle.Install[0].Command)
}

func TestValidateRejectsBadKind(t *testing.T) {
	_, err := Validate(&Descriptor{SchemaVersion: "3", Kind: "agent"})
	require.ErrorContains(t, err, "kind")
}

func TestValidateInjectRequiresMatchingPhaseAllow(t *testing.T) {
	needsYAML := func(needs string) *Descriptor {
		d, err := Decode([]byte("schemaVersion: \"3\"\nkind: mixin\ncapabilities:\n" + needs))
		require.NoError(t, err)
		return d
	}

	// An install credential's inject domain absent from the install
	// phase's allow list is refused.
	d := needsYAML(`
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime: {allow: [api.example.com]}
  - type: com.docker.sandbox/credential@1
    config:
      service: corp-registry
      phase: install
      apiKey:
        name: NPM_TOKEN
        inject:
          - {domain: npm.corp.example.com, header: Authorization}
`)
	_, err := Validate(d)
	require.ErrorContains(t, err, "install allow list")

	// The same inject under runtime with its domain allowed passes.
	d = needsYAML(`
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime: {allow: [api.example.com]}
  - type: com.docker.sandbox/credential@1
    config:
      service: example
      phase: runtime
      apiKey:
        name: EXAMPLE_API_KEY
        inject:
          - {domain: api.example.com, header: x-api-key}
`)
	_, err = Validate(d)
	require.NoError(t, err)

	// A bare "*" allow grants every host, so any inject domain is covered
	// (the builder-kit shape: unrestricted egress declared honestly).
	d.Capabilities[0].Config = map[string]any{"runtime": map[string]any{"allow": []any{"*"}}}
	_, err = Validate(d)
	require.NoError(t, err)

	// Narrower glob patterns are deliberately NOT expanded by validation:
	// the inject domain must appear literally or be covered by a bare
	// wildcard, so a suffix glob alone does not satisfy the check.
	d.Capabilities[0].Config = map[string]any{"runtime": map[string]any{"allow": []any{"*.example.com"}}}
	_, err = Validate(d)
	require.ErrorContains(t, err, "runtime allow list")
}

func TestValidateNeedEntries(t *testing.T) {
	base := func(needs ...Capability) *Descriptor {
		return &Descriptor{SchemaVersion: "3", Kind: KindMixin, Capabilities: needs}
	}

	// The known config-less type: valid bare.
	_, err := Validate(base(Capability{Type: CapabilityKitRegistry}))
	require.NoError(t, err)

	// Unknown types are the extension point working: type syntax is
	// enforced, the host decides at resolve whether it provides them.
	_, err = Validate(base(Capability{Type: "com.example.host/gpu-transcoder@2", Optional: true, Config: map[string]any{"codec": "av1"}}))
	require.NoError(t, err)

	// Type syntax: namespace/name@integer-version. The offending path is
	// structured data on the FieldError, joinable with Positions.
	for _, bad := range []string{"kit-registry", "com.docker.sandbox/kit-registry", "com.docker.sandbox/kit-registry@0", "com.docker.sandbox/KitRegistry@1", "@1"} {
		_, err = Validate(base(Capability{Type: bad}))
		require.Error(t, err, "type %q must be rejected", bad)
		var fieldErr *FieldError
		require.ErrorAs(t, err, &fieldErr)
		require.Equal(t, "capabilities[0].type", fieldErr.Path)
	}

	// A singleton type declared twice is refused.
	_, err = Validate(base(Capability{Type: CapabilityKitRegistry}, Capability{Type: CapabilityKitRegistry}))
	require.ErrorContains(t, err, "already declared")

	// A capability's own fields stay strictly decoded: recording config
	// presence must not open the struct to typos.
	_, err = Decode([]byte("schemaVersion: \"3\"\nkind: workload\ndisplayName: Demo\n" +
		"provides: [\"demo@1.0.0\"]\ncapabilities:\n  - type: " + CapabilityPrivileged + "\n    optionl: true\n"))
	require.ErrorContains(t, err, "optionl")

	// Merge unions a set into one workload-kinded descriptor, so a
	// mixin's platform claim would be indistinguishable from the
	// workload's own by the time an artifact is judged.
	mixin := base(Capability{Type: CapabilitySbx})
	mixin.Kind = KindMixin
	_, err = Validate(mixin)
	require.ErrorContains(t, err, "workload-only")

	set := base(Capability{Type: CapabilitySbx})
	set.Kind = KindSet
	set.Kits = []Kit{{Ref: "example.com/demo:1"}}
	_, err = Validate(set)
	require.NoError(t, err, "a set's kind is derived from its members later")

	// Config-less types take no config.
	_, err = Validate(base(Capability{Type: CapabilityKitRegistry, Config: map[string]any{"x": 1}}))
	require.ErrorContains(t, err, "takes no config")
	_, err = Validate(base(Capability{Type: CapabilityPrivileged, Config: map[string]any{"x": 1}}))
	require.ErrorContains(t, err, "takes no config")

	// The empty and null spellings are config values too, which these
	// types' schemas reject; both decode to something the map alone
	// cannot tell from an omitted key, so Go would otherwise accept a
	// descriptor JSON Schema refuses.
	for _, spelling := range []string{"{}", "null", `{"x": 1}`} {
		var c Capability
		require.NoError(t, yaml.Unmarshal([]byte("type: "+CapabilityPrivileged+"\nconfig: "+spelling+"\n"), &c))
		_, err = Validate(base(c))
		require.ErrorContains(t, err, "takes no config", "yaml config: %s", spelling)

		var j Capability
		require.NoError(t, json.Unmarshal([]byte(`{"type":"`+CapabilityPrivileged+`","config":`+spelling+`}`), &j))
		_, err = Validate(base(j))
		require.ErrorContains(t, err, "takes no config", "json config: %s", spelling)
	}

	// Known configs decode strictly: an unknown field is an error, not
	// silently ignored — this is where a typo in a permission surfaces.
	_, err = Validate(base(Capability{Type: CapabilityVolume, Config: map[string]any{"path": "/data", "sizee": "2g"}}))
	require.ErrorContains(t, err, "sizee")

	// Instance-shaped types repeat — per distinct thing.
	_, err = Validate(base(
		Capability{Type: CapabilityVolume, Config: map[string]any{"path": "/a"}},
		Capability{Type: CapabilityVolume, Config: map[string]any{"path": "/b"}},
	))
	require.NoError(t, err)
	_, err = Validate(base(
		Capability{Type: CapabilityVolume, Config: map[string]any{"path": "/a"}},
		Capability{Type: CapabilityVolume, Config: map[string]any{"path": "/a"}},
	))
	require.ErrorContains(t, err, "identical to capabilities[0]")

	// Same service, same phase, twice: refused even with differing config.
	_, err = Validate(base(
		Capability{Type: CapabilityCredential, Config: map[string]any{"service": "github", "phase": "runtime", "apiKey": map[string]any{"name": "A"}}},
		Capability{Type: CapabilityCredential, Config: map[string]any{"service": "github", "phase": "runtime", "apiKey": map[string]any{"name": "B"}}},
	))
	require.ErrorContains(t, err, "already declared")

	// Credential phase is mandatory and closed.
	_, err = Validate(base(Capability{Type: CapabilityCredential, Config: map[string]any{"service": "github", "apiKey": map[string]any{"name": "A"}}}))
	require.ErrorContains(t, err, "phase")
}

func TestNeedSurfaceAndGate(t *testing.T) {
	// Config participates in the surface: same type with different
	// config is a different grant.
	plain := SurfaceOf(&Descriptor{Capabilities: []Capability{{Type: "com.example/thing@1"}}})
	withCfg := SurfaceOf(&Descriptor{Capabilities: []Capability{{Type: "com.example/thing@1", Config: map[string]any{"path": "/data"}}}})
	require.NotEqual(t, plain.Services, withCfg.Services)

	// Equal configs produce equal entries regardless of declaration
	// details like Optional or Description.
	again := SurfaceOf(&Descriptor{Capabilities: []Capability{{Type: "com.example/thing@1", Optional: true, Description: "d", Config: map[string]any{"path": "/data"}}}})
	require.Equal(t, withCfg.Services, again.Services)

	// A config change gates as a services widening.
	ws := DiffWidenings(withCfg, SurfaceOf(&Descriptor{Capabilities: []Capability{{Type: "com.example/thing@1", Config: map[string]any{"path": "/other"}}}}))
	require.Len(t, ws, 1)
	require.Equal(t, "services", ws[0].Category)

	// The platform type asks the host to launch the workload a
	// particular way and to read an identity the image already states,
	// so it grants nothing and never stops an update for approval.
	sbx := SurfaceOf(&Descriptor{Capabilities: []Capability{{Type: CapabilitySbx}}})
	require.Empty(t, sbx.Services)
	require.Equal(t, Surface{}, sbx)
	require.Empty(t, DiffWidenings(SurfaceOf(&Descriptor{}), sbx))

	// Well-known types keep their direction-aware fields: privilege is a
	// boolean, network allows diff per entry.
	priv := SurfaceOf(&Descriptor{Capabilities: []Capability{{Type: CapabilityPrivileged}}})
	require.True(t, priv.Privileged)
	net := SurfaceOf(&Descriptor{Capabilities: []Capability{{
		Type:   CapabilityNetworkPolicy,
		Config: map[string]any{"runtime": map[string]any{"allow": []any{"api.example.com"}}},
	}}})
	require.Equal(t, []string{"api.example.com"}, net.NetworkRuntimeAllow)
	require.Empty(t, net.Services, "well-known decodable types do not double as service entries")
}

func TestValidateArgPhaseExclusivity(t *testing.T) {
	d := &Descriptor{
		SchemaVersion: "3",
		Kind:          KindMixin,
		Args: map[string]Arg{
			"version": {Env: "V", BuildArg: "V"},
		},
	}
	_, err := Validate(d)
	require.ErrorContains(t, err, "mutually exclusive")
}

func TestValidateAgentContextFilenameIsWorkloadOnly(t *testing.T) {
	d := &Descriptor{
		SchemaVersion: "3",
		Kind:          KindMixin,
		Capabilities: []Capability{{
			Type:   CapabilityAgentContext,
			Config: map[string]any{"filename": "AGENTS.md"},
		}},
	}
	_, err := Validate(d)
	require.ErrorContains(t, err, "workload-kit-only")
}

func TestValidateUSBExactlyOneMatcher(t *testing.T) {
	d := &Descriptor{
		SchemaVersion: "3",
		Kind:          KindMixin,
		Capabilities: []Capability{{
			Type:   CapabilityUSBDevice,
			Config: map[string]any{"vendorId": "0x2341", "productId": "0x0043", "class": "cdc-acm"},
		}},
	}
	_, err := Validate(d)
	require.ErrorContains(t, err, "not both")
}

func TestValidateRawRejectsUndeclaredArgReference(t *testing.T) {
	y := `schemaVersion: "3"
kind: mixin
provides: ["gh@${{ kit.args.version }}"]
`
	d, err := Decode([]byte(y))
	require.NoError(t, err)
	_, err = ValidateRaw([]byte(y), d)
	require.ErrorContains(t, err, "declares no arg")
}

func TestValidateRawSizeBudget(t *testing.T) {
	big := "schemaVersion: \"3\"\nkind: mixin\ndescription: " +
		strings.Repeat("x", SizeErrorBytes)
	d, err := Decode([]byte(big))
	require.NoError(t, err)
	_, err = ValidateRaw([]byte(big), d)
	require.ErrorContains(t, err, "budget")
}

func TestResolveArgs(t *testing.T) {
	dflt := "2.98.0"
	decls := map[string]Arg{
		"version": {Default: &dflt, Pattern: `^[0-9]+\.[0-9]+\.[0-9]+$`},
		"team":    {Required: true, Enum: []string{"alpha", "beta"}},
	}

	_, err := ResolveArgs(decls, nil)
	require.ErrorContains(t, err, "required")

	values, err := ResolveArgs(decls, map[string]string{"team": "alpha"})
	require.NoError(t, err)
	require.Equal(t, "2.98.0", values["version"])
	require.Equal(t, "alpha", values["team"])

	_, err = ResolveArgs(decls, map[string]string{"team": "gamma"})
	require.ErrorContains(t, err, "not one of")

	_, err = ResolveArgs(decls, map[string]string{"team": "alpha", "version": "latest"})
	require.ErrorContains(t, err, "pattern")

	_, err = ResolveArgs(decls, map[string]string{"team": "alpha", "typo": "x"})
	require.ErrorContains(t, err, "not declared")
}

func TestExpandLifecycleLeavesShellVarsAlone(t *testing.T) {
	d, err := Decode([]byte(`schemaVersion: "3"
kind: mixin
args:
  timeout:
    default: "30000"
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      files:
        - path: /home/agent/.config/bt.json
          content: '{"timeout": ${{ kit.args.timeout }}, "workdir": "${WORKDIR}"}'
`))
	require.NoError(t, err)

	values, err := ResolveArgs(d.Args, nil)
	require.NoError(t, err)

	lifecycle, err := LifecycleOf(d.Capabilities)
	require.NoError(t, err)
	expanded, err := ExpandLifecycle(lifecycle, values)
	require.NoError(t, err)
	require.Equal(t, `{"timeout": 30000, "workdir": "${WORKDIR}"}`, expanded.Files[0].Content)
	// The declared lifecycle is never rewritten.
	require.Contains(t, lifecycle.Files[0].Content, "${{ kit.args.timeout }}")
}

func TestExpandBuildArgsOnlyTouchesBuildPhase(t *testing.T) {
	d := decodeValid(t, ghYAML)
	values, err := ResolveArgs(d.Args, map[string]string{"version": "2.99.0"})
	require.NoError(t, err)

	published, err := ExpandBuildArgs([]byte(ghYAML), d.Args, values)
	require.NoError(t, err)
	require.Contains(t, string(published), `provides: ["gh@2.99.0"]`)

	// The published descriptor still validates and now carries a literal
	// provide.
	pd, err := Decode(published)
	require.NoError(t, err)
	_, err = ValidatePublished(published, pd)
	require.NoError(t, err)

	// The authored form is not publishable as-is: the provide still holds
	// a reference.
	ad, err := Decode([]byte(ghYAML))
	require.NoError(t, err)
	_, err = ValidatePublished([]byte(ghYAML), ad)
	require.ErrorContains(t, err, "expansion did not run")
	p, err := ParseProvide(pd.Provides[0])
	require.NoError(t, err)
	require.Equal(t, Provide{Name: "com.docker.kit/gh", Version: "2.99.0"}, p,
		"parsed names are normalized: bare names gain the default namespace")
}

func TestPositionsAndFieldErrors(t *testing.T) {
	y := `schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/port@1
    config: {name: web, container: 99999}
args:
  version:
    pattern: "["
`
	positions := Positions([]byte(y))
	port, ok := positions["capabilities[0].config.container"]
	require.True(t, ok)
	require.Equal(t, 5, port.Line)
	pattern, ok := positions["args.version.pattern"]
	require.True(t, ok)
	require.Equal(t, 8, pattern.Line)

	d, err := Decode([]byte(y))
	require.NoError(t, err)
	_, err = Validate(d)
	require.Error(t, err)

	// The validation error names the offending element's path, joinable
	// with Positions to point at source lines.
	var fieldErr *FieldError
	require.ErrorAs(t, err, &fieldErr)
	require.Equal(t, "capabilities[0].config.container", fieldErr.Path)
	require.Contains(t, positions, fieldErr.Path)

	// Fix the port; the next failure is the arg pattern, with its path.
	d.Capabilities[0].Config["container"] = 8080
	_, err = Validate(d)
	require.ErrorAs(t, err, &fieldErr)
	require.Equal(t, "args.version.pattern", fieldErr.Path)
}

func TestRequireVersionedProvides(t *testing.T) {
	// Explicitly versioned provides pass.
	require.NoError(t, RequireVersionedProvides(&Descriptor{
		Kind: KindMixin, Provides: []string{"gh@2.98.0"},
	}))

	// An unversioned provide with no fallback fails, naming the entry.
	err := RequireVersionedProvides(&Descriptor{
		Kind: KindMixin, Provides: []string{"gh"},
	})
	require.Error(t, err)
	var fieldErr *FieldError
	require.ErrorAs(t, err, &fieldErr)
	require.Equal(t, "provides[0]", fieldErr.Path)

	// The version: fallback satisfies the rule.
	require.NoError(t, RequireVersionedProvides(&Descriptor{
		Kind: KindMixin, Provides: []string{"gh"}, Version: "2.98.0",
	}))

	// No provides at all: nothing matchable, nothing to version.
	require.NoError(t, RequireVersionedProvides(&Descriptor{Kind: KindMixin}))
}

func TestCapabilityNamespaces(t *testing.T) {
	// Bare names normalize into the default namespace, the way bare
	// image names normalize into docker.io/library.
	p, err := ParseProvide("gh@2.98.0")
	require.NoError(t, err)
	require.Equal(t, "com.docker.kit/gh", p.Name)

	// The explicit spelling is the same capability.
	r, err := ParseRequire("com.docker.kit/gh >= 2.0.0")
	require.NoError(t, err)
	require.True(t, Satisfies(p, r))

	// A third party's namespace never matches the default one.
	foreign, err := ParseRequire("com.example/gh >= 2.0.0")
	require.NoError(t, err)
	require.False(t, Satisfies(p, foreign))

	// Qualified provides parse and match within their namespace.
	fp, err := ParseProvide("com.example/gh@3.0.0")
	require.NoError(t, err)
	require.True(t, Satisfies(fp, foreign))

	// Display strips only the default namespace.
	require.Equal(t, "gh", DisplayCapabilityName(p.Name))
	require.Equal(t, "com.example/gh", DisplayCapabilityName(fp.Name))

	// Namespace syntax is validated.
	_, err = ParseProvide("Com.Example/gh")
	require.ErrorContains(t, err, "invalid capability name")
	_, err = ParseProvide("com.example/gh/extra")
	require.ErrorContains(t, err, "invalid capability name")
}

func TestVersions(t *testing.T) {
	require.Equal(t, -1, CompareVersions("2.9.0", "2.98.0"))
	require.Equal(t, 1, CompareVersions("13", "2"))
	require.False(t, IsVersion("v2.98.0"), "v-prefixed strings are not versions")
	require.True(t, IsVersion("2.98.0"))

	p, err := ParseProvide("debian@13")
	require.NoError(t, err)
	r, err := ParseRequire("debian >= 13")
	require.NoError(t, err)
	require.True(t, Satisfies(p, r))

	r2, err := ParseRequire("debian >= 14")
	require.NoError(t, err)
	require.False(t, Satisfies(p, r2))

	// Unversioned provide satisfies only an unconstrained require.
	unv, err := ParseProvide("browser-automation")
	require.NoError(t, err)
	anyReq, err := ParseRequire("browser-automation")
	require.NoError(t, err)
	require.True(t, Satisfies(unv, anyReq))
	minReq, err := ParseRequire("browser-automation >= 1.0.0")
	require.NoError(t, err)
	require.False(t, Satisfies(unv, minReq))
}

func TestParseRequireConstraints(t *testing.T) {
	// Legacy minimum form is unchanged.
	r, err := ParseRequire("node >= 20.0.0")
	require.NoError(t, err)
	require.Equal(t, "com.docker.kit/node", r.Name)
	require.Equal(t, []Constraint{{Op: OpGTE, Version: "20.0.0"}}, r.Constraints)

	// Upper bound only.
	r, err = ParseRequire("api <= 1.0.0")
	require.NoError(t, err)
	require.Equal(t, []Constraint{{Op: OpLTE, Version: "1.0.0"}}, r.Constraints)

	// Lower and upper, with and without spaces around the comma/ops.
	for _, s := range []string{
		"node >= 20.0.0, < 21.0.0",
		"node>=20.0.0,<21.0.0",
		"node > 1.0.0, <= 2.0.0",
	} {
		r, err = ParseRequire(s)
		require.NoError(t, err, s)
		require.Len(t, r.Constraints, 2, s)
	}
	r, err = ParseRequire("node >= 20.0.0, < 21.0.0")
	require.NoError(t, err)
	require.Equal(t, []Constraint{
		{Op: OpGTE, Version: "20.0.0"},
		{Op: OpLT, Version: "21.0.0"},
	}, r.Constraints)

	// Pin.
	r, err = ParseRequire("gh = 2.98.0")
	require.NoError(t, err)
	require.Equal(t, []Constraint{{Op: OpEQ, Version: "2.98.0"}}, r.Constraints)

	// Unconstrained.
	r, err = ParseRequire("shell")
	require.NoError(t, err)
	require.Empty(t, r.Constraints)

	// Reject malformed tails and unsatisfiable constraint sets.
	for _, s := range []string{
		"node >=",
		"node >= 20,",
		"node ~= 1.0.0",
		">= 1.0.0",
		"node >= 20, <",
		"node >= v1.0.0",
		"node >= 2.0.0, < 1.0.0",
		"node > 1.0.0, <= 1.0.0",
		"node >= 1.0.0, < 1.0.0",
		"node = 1.0.0, = 2.0.0",
		"node = 1.0.0, >= 2.0.0",
		"node = 2.0.0, < 2.0.0",
	} {
		_, err = ParseRequire(s)
		require.Error(t, err, s)
	}

	// Tight but satisfiable bounds still parse.
	for _, s := range []string{
		"node >= 1.0.0, <= 1.0.0",
		"node = 1.0.0, >= 1.0.0, <= 1.0.0",
		"node > 1.0.0, < 2.0.0",
	} {
		_, err = ParseRequire(s)
		require.NoError(t, err, s)
	}
}

func TestSatisfiesRanges(t *testing.T) {
	p, err := ParseProvide("node@20.5.0")
	require.NoError(t, err)

	inRange, err := ParseRequire("node >= 20.0.0, < 21.0.0")
	require.NoError(t, err)
	require.True(t, Satisfies(p, inRange))

	tooLow, err := ParseRequire("node > 20.5.0, < 21.0.0")
	require.NoError(t, err)
	require.False(t, Satisfies(p, tooLow))

	tooHigh, err := ParseRequire("node >= 20.0.0, < 20.5.0")
	require.NoError(t, err)
	require.False(t, Satisfies(p, tooHigh))

	upperOnly, err := ParseRequire("node <= 1.0.0")
	require.NoError(t, err)
	require.False(t, Satisfies(p, upperOnly))

	pinOK, err := ParseRequire("node = 20.5.0")
	require.NoError(t, err)
	require.True(t, Satisfies(p, pinOK))

	pinBad, err := ParseRequire("node = 20.5.1")
	require.NoError(t, err)
	require.False(t, Satisfies(p, pinBad))

	// Any constraint rejects an unversioned provide.
	unv, err := ParseProvide("node")
	require.NoError(t, err)
	require.False(t, Satisfies(unv, upperOnly))
	require.False(t, Satisfies(unv, pinOK))
}

// Every other display field is free text a consumer chooses how to show.
// An icon is fetched and rendered, so the scheme is a boundary rather
// than decoration: a javascript: or data: URL reaching an <img> is code
// execution, and file: is a local read.
func TestValidateIconURL(t *testing.T) {
	withIcon := func(icon string) *Descriptor {
		return &Descriptor{SchemaVersion: "3", Kind: KindMixin, IconURL: icon}
	}

	_, err := Validate(withIcon("https://example.com/kit.svg"))
	require.NoError(t, err)

	// Absent is the common case and stays valid.
	_, err = Validate(withIcon(""))
	require.NoError(t, err)

	for _, icon := range []string{
		"javascript:alert(1)",
		"data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=",
		"file:///etc/passwd",
		"http://example.com/kit.svg",
		"./icon.svg",
		"example.com/kit.svg",
	} {
		_, err := Validate(withIcon(icon))
		require.Error(t, err, "icon %q must be refused", icon)
		require.ErrorContains(t, err, "absolute https URL")
	}
}

// A kit declares where its agent reads skills because only the kit knows:
// an agent behind a wrapper, or one the runtime has never heard of, reads
// from a path no host-side table can predict.
func TestAgentSkillsValidation(t *testing.T) {
	withSkills := func(paths ...string) *Descriptor {
		d := &Descriptor{SchemaVersion: "3", Kind: KindMixin}
		for _, p := range paths {
			d.Capabilities = append(d.Capabilities, Capability{
				Type:   CapabilityAgentSkills,
				Config: map[string]any{"path": p},
			})
		}
		return d
	}
	withMode := func(path, mode string) *Descriptor {
		return &Descriptor{SchemaVersion: "3", Kind: KindMixin, Capabilities: []Capability{{
			Type:   CapabilityAgentSkills,
			Config: map[string]any{"path": path, "mode": mode},
		}}}
	}

	_, err := Validate(withSkills("/home/agent/.claude/skills"))
	require.NoError(t, err)

	// Instance-shaped: one composition may host two agents that read
	// skills from different places.
	_, err = Validate(withSkills("/home/agent/.claude/skills", "/home/agent/.agents/skills"))
	require.NoError(t, err)

	_, err = Validate(withSkills("relative/skills"))
	require.ErrorContains(t, err, "must be absolute")

	// Lexical aliases are distinct strings for one destination, so they
	// could evade the duplicate check and declare a second mode for a
	// path already claimed. Canonical form is validated, not normalized
	// in, so the descriptor a user reads is the request the runtime sees.
	for _, alias := range []string{"/x/../skills", "/skills/", "/skills/./sub", "//skills", "/"} {
		_, err = Validate(withSkills(alias))
		require.ErrorContains(t, err, "canonical", "alias %q must be rejected", alias)
	}

	// Identical requests are caught by the generic duplicate rule.
	_, err = Validate(withSkills("/home/agent/.claude/skills", "/home/agent/.claude/skills"))
	require.ErrorContains(t, err, "identical to capabilities[0]")

	_, err = Validate(withMode("/home/agent/.claude/skills", SkillsReadWrite))
	require.NoError(t, err)
	_, err = Validate(withMode("/home/agent/.claude/skills", "rw"))
	require.ErrorContains(t, err, "mode must be")

	// One path claimed twice with different modes contradicts itself
	// about a single mount, so it is rejected rather than silently
	// resolved to one of them.
	contradictory := &Descriptor{SchemaVersion: "3", Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityAgentSkills, Config: map[string]any{"path": "/s", "mode": SkillsReadOnly}},
		{Type: CapabilityAgentSkills, Config: map[string]any{"path": "/s", "mode": SkillsReadWrite}},
	}}
	_, err = Validate(contradictory)
	require.ErrorContains(t, err, "already declared")

	// An omitted mode is the read-only default, which is what keeps a
	// permissive host from handing write access to a kit that never
	// asked for it.
	require.Equal(t, SkillsReadOnly, SkillsMode(AgentSkills{Path: "/s"}))
	require.Equal(t, SkillsReadWrite, SkillsMode(AgentSkills{Path: "/s", Mode: SkillsReadWrite}))
}

// The store is a host directory the user filled with `sbx skills import`,
// so handing it to a kit is a grant the gate has to name — and name
// distinctly from storage, which grants the sandbox space of its own.
func TestAgentSkillsSurfaceAndGate(t *testing.T) {
	granted := SurfaceOf(&Descriptor{SchemaVersion: "3", Kind: KindMixin})
	candidate := SurfaceOf(&Descriptor{SchemaVersion: "3", Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityAgentSkills, Config: map[string]any{"path": "/home/agent/.claude/skills"}},
	}})

	require.Equal(t, []string{"/home/agent/.claude/skills"}, candidate.SkillsPaths)
	require.Empty(t, candidate.StoragePaths, "a skills mount is not storage the sandbox was given")

	require.Equal(t,
		[]Widening{{Category: "skills", Detail: "/home/agent/.claude/skills"}},
		DiffWidenings(granted, candidate))

	// The same declaration twice over is not a widening.
	require.Empty(t, DiffWidenings(candidate, candidate))

	// Raising an existing path to readwrite is: reading the user's shared
	// skills and rewriting them for every later sandbox differ.
	writable := SurfaceOf(&Descriptor{SchemaVersion: "3", Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityAgentSkills, Config: map[string]any{
			"path": "/home/agent/.claude/skills", "mode": SkillsReadWrite}},
	}})
	require.Equal(t, []string{"/home/agent/.claude/skills"}, writable.SkillsWritePaths)
	require.Equal(t,
		[]Widening{{Category: "skills.write", Detail: "/home/agent/.claude/skills"}},
		DiffWidenings(candidate, writable))

	// Dropping back to read-only asks for less, which never gates.
	require.Empty(t, DiffWidenings(writable, candidate))
}

// §9.2 holds for every published descriptor — including third-party
// artifacts the frontend never saw — so the published-form validator is
// where the rule lives, not only the frontend's build path.
func TestValidatePublishedRequiresVersionedProvides(t *testing.T) {
	raw := []byte(`{"schemaVersion":"3","kind":"workload","provides":["demo"]}`)
	d, err := Decode(raw)
	require.NoError(t, err)

	_, err = ValidatePublished(raw, d)
	require.ErrorContains(t, err, "has no version")

	// The descriptor-level version: fallback satisfies it.
	raw = []byte(`{"schemaVersion":"3","kind":"workload","version":"1.0.0","provides":["demo"]}`)
	d, err = Decode(raw)
	require.NoError(t, err)
	_, err = ValidatePublished(raw, d)
	require.NoError(t, err)
}

// AgentSkillsOf is what a runtime consumes, and Optional is the field it
// consults to refuse a required entry it cannot satisfy while skipping an
// optional one — dropping it during decode would turn every refusal into
// a skip.
func TestAgentSkillsOfCarriesTheFieldsRuntimesActOn(t *testing.T) {
	needs := []Capability{
		{Type: CapabilityVolume, Config: map[string]any{"path": "/data"}},
		{Type: CapabilityAgentSkills, Description: "claude reads here",
			Config: map[string]any{"path": "/home/agent/.claude/skills", "mode": SkillsReadWrite}},
		{Type: CapabilityAgentSkills, Optional: true,
			Config: map[string]any{"path": "/home/agent/.agents/skills"}},
	}

	got, err := AgentSkillsOf(needs)
	require.NoError(t, err)
	require.Equal(t, []AgentSkillsCapability{
		{
			AgentSkills: AgentSkills{Path: "/home/agent/.claude/skills", Mode: SkillsReadWrite},
			Description: "claude reads here",
		},
		{
			AgentSkills: AgentSkills{Path: "/home/agent/.agents/skills"},
			Optional:    true,
		},
	}, got, "declaration order, Optional, and Description must survive decoding")

	_, err = AgentSkillsOf([]Capability{{Type: CapabilityAgentSkills, Config: map[string]any{"path": 7}}})
	require.Error(t, err)
}

// Access lives in its own field rather than being marked on the path,
// because a path is arbitrary text: any suffix meaning "writable" is also
// a path somebody could ask for, and then one grant would silently cover
// the other.
func TestAgentSkillsAccessCannotBeConfusedWithAPath(t *testing.T) {
	// A kit granted write on /x.
	granted := SurfaceOf(&Descriptor{SchemaVersion: "3", Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityAgentSkills, Config: map[string]any{"path": "/x", "mode": SkillsReadWrite}},
	}})

	// A different kit asking to read a path that merely looks like the
	// first one's write marker. It is a new path and must gate.
	lookalike := SurfaceOf(&Descriptor{SchemaVersion: "3", Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityAgentSkills, Config: map[string]any{"path": "/x (readwrite)"}},
	}})

	require.Equal(t,
		[]Widening{{Category: "skills", Detail: "/x (readwrite)"}},
		DiffWidenings(granted, lookalike))
}

// Expansion used to paste the value into the serialized descriptor, so a
// quote ended the enclosing string early and a backslash sequence was
// reread as an escape — `C:\new\text` reached the container as `C:`,
// newline, `ew`, tab, `ext`.
func TestExpandCreateArgsPreservesValuesVerbatim(t *testing.T) {
	raw := []byte(`{"schemaVersion":"3","kind":"mixin","capabilities":[{"type":"com.docker.sandbox/lifecycle@1","config":{"files":[{"path":"/home/agent/result.txt","content":"${{ kit.args.text }}"}]}}]}`)
	decls := map[string]Arg{"text": {}}

	for _, value := range []string{
		`say "hello"`,
		`C:\new\text`,
		"line\nbreak",
		`{"json":"looking"}`,
		`trailing\`,
		"tab\there",
	} {
		out, err := ExpandCreateArgs(raw, decls, map[string]string{"text": value})
		require.NoError(t, err, "value %q", value)

		d, err := Decode(out)
		require.NoError(t, err, "value %q", value)
		lc, err := LifecycleOf(d.Capabilities)
		require.NoError(t, err)
		require.Len(t, lc.Files, 1)
		require.Equal(t, value, lc.Files[0].Content, "value %q must survive verbatim", value)
	}
}

// Typed validation is deferred for parameterized capabilities precisely so
// a numeric arg can reach a numeric field after expansion. Pasting text
// left it quoted, so the field never decoded.
func TestExpandCreateArgsGivesANumericArgToANumericField(t *testing.T) {
	raw := []byte(`{"schemaVersion":"3","kind":"mixin","capabilities":[{"type":"com.docker.sandbox/port@1","config":{"container":"${{ kit.args.port }}"}}]}`)

	out, err := ExpandCreateArgs(raw, map[string]Arg{"port": {}}, map[string]string{"port": "8080"})
	require.NoError(t, err)
	d, err := Decode(out)
	require.NoError(t, err)
	ports, err := PortsOf(d.Capabilities)
	require.NoError(t, err)
	require.Len(t, ports, 1)
	require.Equal(t, 8080, ports[0].Container)
}

// Only exact integer and boolean literals adopt a type; anything a reader
// would expect to stay text does, so a version or a zero-padded code still
// reaches a string field intact.
func TestExpandCreateArgsKeepsAmbiguousLiteralsAsStrings(t *testing.T) {
	raw := []byte(`{"schemaVersion":"3","kind":"mixin","capabilities":[{"type":"com.docker.sandbox/lifecycle@1","config":{"files":[{"path":"/x","content":"${{ kit.args.v }}"}]}}]}`)

	for _, value := range []string{"1.0", "007", "1e5", "0x10", "+1"} {
		out, err := ExpandCreateArgs(raw, map[string]Arg{"v": {}}, map[string]string{"v": value})
		require.NoError(t, err)
		d, err := Decode(out)
		require.NoError(t, err, "value %q", value)
		lc, err := LifecycleOf(d.Capabilities)
		require.NoError(t, err)
		require.Equal(t, value, lc.Files[0].Content, "value %q must stay a string", value)
	}
}

// A reference embedded in a larger string is substitution, not typing: the
// result is always text.
func TestExpandCreateArgsSubstitutesWithinAString(t *testing.T) {
	raw := []byte(`{"schemaVersion":"3","kind":"mixin","capabilities":[{"type":"com.docker.sandbox/lifecycle@1","config":{"files":[{"path":"/x","content":"port ${{ kit.args.port }} is \"open\""}]}}]}`)

	out, err := ExpandCreateArgs(raw, map[string]Arg{"port": {}}, map[string]string{"port": "8080"})
	require.NoError(t, err)
	d, err := Decode(out)
	require.NoError(t, err)
	lc, err := LifecycleOf(d.Capabilities)
	require.NoError(t, err)
	require.Equal(t, `port 8080 is "open"`, lc.Files[0].Content)
}

// An omitted transport IS tcp, so declaring both spellings of one port is
// the duplicate the check exists to catch.
func TestValidateRejectsDuplicateDefaultTransportPorts(t *testing.T) {
	d := &Descriptor{SchemaVersion: "3", Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityPort, Config: map[string]any{"container": 18993}},
		{Type: CapabilityPort, Config: map[string]any{"container": 18993, "transport": "tcp"}},
	}}
	_, err := Validate(d)
	require.ErrorContains(t, err, "port 18993 already declared")

	// Different transports on one port stay legal.
	d.Capabilities[1].Config = map[string]any{"container": 18993, "transport": "udp"}
	_, err = Validate(d)
	require.NoError(t, err)
}

// A key names something the author chose, so references reach map keys
// too — the credential capability's `structure` is an author-shaped
// document, not a grammar-defined one. Textual expansion reached keys for
// free; substituting inside the decoded document has to do it on purpose.
func TestExpandCreateArgsExpandsMapKeys(t *testing.T) {
	raw := []byte(`{"structure":{"${{ kit.args.name }}":{"nested":"${{ kit.args.name }} value"}}}`)

	out, err := ExpandCreateArgs(raw, map[string]Arg{"name": {}}, map[string]string{"name": "profile"})
	require.NoError(t, err)
	require.JSONEq(t, `{"structure":{"profile":{"nested":"profile value"}}}`, string(out))
}

// A key is text whatever the value looks like: a numeric value in key
// position must not become a number, which is not a legal key.
func TestExpandCreateArgsKeepsMapKeysTextual(t *testing.T) {
	raw := []byte(`{"structure":{"${{ kit.args.port }}":"open"}}`)

	out, err := ExpandCreateArgs(raw, map[string]Arg{"port": {}}, map[string]string{"port": "8080"})
	require.NoError(t, err)
	require.JSONEq(t, `{"structure":{"8080":"open"}}`, string(out))
}

// cpu is a float64, so integer-only typing would leave fractional CPU
// unreachable by an arg. A spelling that survives a round trip adopts its
// type; one that does not stays text.
func TestExpandCreateArgsTypesFractionalNumbers(t *testing.T) {
	raw := []byte(`{"schemaVersion":"3","kind":"mixin","capabilities":[{"type":"com.docker.sandbox/resources@1","config":{"cpu":"${{ kit.args.cpus }}"}}]}`)

	out, err := ExpandCreateArgs(raw, map[string]Arg{"cpus": {}}, map[string]string{"cpus": "1.5"})
	require.NoError(t, err)
	d, err := Decode(out)
	require.NoError(t, err)
	r, err := ResourcesOf(d.Capabilities)
	require.NoError(t, err)
	require.InDelta(t, 1.5, r.CPU, 0)

	// "1.0" renders back as "1", so it is the ambiguous spelling a version
	// wears and stays a string — which a float field then rejects loudly.
	out, err = ExpandCreateArgs(raw, map[string]Arg{"cpus": {}}, map[string]string{"cpus": "1.0"})
	require.NoError(t, err)
	require.Contains(t, string(out), `"cpu":"1.0"`)
}

// Two keys that expand onto one key would drop a value, and map iteration
// order would decide which — so the descriptor fails instead. Byte-level
// expansion used to leave the duplicate for the decoder to reject; keeping
// the failure is the point, only the reporting moved.
func TestExpandCreateArgsRejectsCollapsedMapKeys(t *testing.T) {
	raw := []byte(`{"structure":{"${{ kit.args.name }}":"from-arg","profile":"literal"}}`)

	_, err := ExpandCreateArgs(raw, map[string]Arg{"name": {}}, map[string]string{"name": "profile"})
	require.ErrorContains(t, err, `collapses two keys onto "profile"`)

	// A key that expands to something unused stays fine.
	out, err := ExpandCreateArgs(raw, map[string]Arg{"name": {}}, map[string]string{"name": "other"})
	require.NoError(t, err)
	require.JSONEq(t, `{"structure":{"other":"from-arg","profile":"literal"}}`, string(out))
}

// NaN and ±Inf survive a ParseFloat/FormatFloat round trip but have no JSON
// spelling, so adopting the type would fail serialization for a value a
// string field would have taken.
func TestExpandCreateArgsKeepsNonFiniteNumbersAsStrings(t *testing.T) {
	raw := []byte(`{"content":"${{ kit.args.v }}"}`)

	for _, value := range []string{"NaN", "+Inf", "-Inf", "Inf", "-nan"} {
		out, err := ExpandCreateArgs(raw, map[string]Arg{"v": {}}, map[string]string{"v": value})
		require.NoError(t, err, "value %q", value)
		require.JSONEq(t, `{"content":"`+value+`"}`, string(out), "value %q must stay a string", value)
	}
}

// policyV2 decodes a descriptor carrying one network-policy@2 config.
func policyV2(t *testing.T, config string) *Descriptor {
	t.Helper()
	d, err := Decode([]byte("schemaVersion: \"3\"\nkind: mixin\ncapabilities:\n" +
		"  - type: com.docker.sandbox/network-policy@2\n    config:\n" + config))
	require.NoError(t, err)
	return d
}

// A bare entry is the whole of what @1 could say, and stays the common
// case: a kit that bounds nothing writes the list it always wrote.
func TestNetworkEntryBareShorthand(t *testing.T) {
	d := policyV2(t, `      runtime:
        allow: [api.example.com, "*.example.org"]
        deny: [telemetry.example.com]
`)
	_, err := Validate(d)
	require.NoError(t, err)

	p, err := NetworkPolicyV2Of(d.Capabilities)
	require.NoError(t, err)
	require.Equal(t, []NetworkEntry{
		{Hosts: []string{"api.example.com"}},
		{Hosts: []string{"*.example.org"}},
	}, p.Runtime.Allow)
	require.Equal(t, []NetworkEntry{{Hosts: []string{"telemetry.example.com"}}}, p.Runtime.Deny)

	for _, e := range p.Runtime.Allow {
		require.False(t, e.Bounded(), "a bare entry grants the connection")
		require.Equal(t, []string{MethodAny}, EntryMethods(e))
		require.Equal(t, []string{"/**"}, EntryPaths(e))
	}
}

// The shorthand round-trips as the string it was written as, so a
// descriptor does not grow an object form its author never used.
func TestNetworkEntryRoundTrip(t *testing.T) {
	var e NetworkEntry
	require.NoError(t, json.Unmarshal([]byte(`"api.example.com"`), &e))
	require.Equal(t, NetworkEntry{Hosts: []string{"api.example.com"}}, e)

	out, err := json.Marshal(e)
	require.NoError(t, err)
	require.JSONEq(t, `"api.example.com"`, string(out))

	// Anything the shorthand cannot express stays an object.
	out, err = json.Marshal(NetworkEntry{Hosts: []string{"a", "b"}})
	require.NoError(t, err)
	require.JSONEq(t, `{"hosts":["a","b"]}`, string(out))

	// A stated-but-empty list has nowhere to go: the shorthand cannot
	// carry it and omitempty drops it, so writing the entry would widen
	// it past what validation accepts. Refused instead of laundered.
	_, err = json.Marshal(NetworkEntry{Hosts: []string{"a"}, Methods: []string{}})
	require.ErrorContains(t, err, "empty methods list")
	_, err = json.Marshal(NetworkEntry{Hosts: []string{"a"}, Methods: []string{"GET"}, Paths: []string{}})
	require.ErrorContains(t, err, "empty paths list")

	out, err = json.Marshal(NetworkEntry{Hosts: []string{"a"}, Methods: []string{"GET"}})
	require.NoError(t, err)
	require.JSONEq(t, `{"hosts":["a"],"methods":["GET"]}`, string(out))
}

// A custom unmarshaler does not inherit the caller's
// DisallowUnknownFields, so the entry has to be strict on its own.
func TestNetworkEntryRejectsUnknownFields(t *testing.T) {
	d := policyV2(t, `      runtime:
        allow:
          - hosts: [api.example.com]
            verbs: [GET]
`)
	_, err := Validate(d)
	require.ErrorContains(t, err, "verbs")
}

// A path says which requests are bounded; without a method it would bound
// none of them while reading like a restriction.
func TestNetworkEntryPathsRequireMethods(t *testing.T) {
	_, err := Validate(policyV2(t, `      runtime:
        allow:
          - hosts: [api.example.com]
            paths: [/v1/**]
`))
	require.ErrorContains(t, err, "states paths without methods")

	// ANY is how the same entry says every method at that path.
	_, err = Validate(policyV2(t, `      runtime:
        allow:
          - hosts: [api.example.com]
            methods: [ANY]
            paths: [/v1/**]
`))
	require.NoError(t, err)
}

func TestNetworkEntryMethodRules(t *testing.T) {
	entry := func(methods string) *Descriptor {
		return policyV2(t, `      runtime:
        allow:
          - hosts: [api.example.com]
            methods: `+methods+"\n")
	}

	for _, test := range []struct{ name, methods, wantErr string }{
		{"named methods", "[GET, HEAD]", ""},
		{"any alone", "[ANY]", ""},
		{"lowercase is not canonical", "[get]", "is not an uppercase HTTP method"},
		{"unknown token", "[FETCH]", "is not an uppercase HTTP method"},
		{"any beside a specific method", "[ANY, GET]", "names ANY beside a specific method"},
		{"repeated method", "[GET, GET]", "repeats method"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Validate(entry(test.methods))
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

// An explicit empty list is not the wildcard: omitted means unbounded, but
// a generated methods: [] granting everything would be the opposite of
// what an empty restriction reads as.
func TestNetworkEntryRejectsPresentButEmptyLists(t *testing.T) {
	_, err := Validate(policyV2(t, `      runtime:
        allow:
          - hosts: [api.example.com]
            methods: []
`))
	require.ErrorContains(t, err, "empty methods list")

	_, err = Validate(policyV2(t, `      runtime:
        allow:
          - hosts: [api.example.com]
            methods: [GET]
            paths: []
`))
	require.ErrorContains(t, err, "empty paths list")

	// A stated methods with paths beside it cannot be fixed by omitting
	// methods — that fails the paths-without-methods rule — so the
	// remedy names both.
	_, err = Validate(policyV2(t, `      runtime:
        allow:
          - hosts: [api.example.com]
            methods: []
            paths: [/v1/**]
`))
	require.ErrorContains(t, err, "omit paths too")

	// null decodes to a nil slice, which validation cannot tell from an
	// omitted field — so a stated methods: null would read as the
	// wildcard and grant every method. Decode normalizes it to the empty
	// list it is, and validation reports it with the whole entry in view.
	for _, test := range []struct{ name, config, wantErr string }{
		{"methods", `      runtime:
        allow:
          - hosts: [api.example.com]
            methods: null
`, "empty methods list"},
		{"paths", `      runtime:
        allow:
          - hosts: [api.example.com]
            methods: [GET]
            paths: null
`, "empty paths list"},
		// hosts is required, so omission is no remedy and the message
		// does not suggest it.
		{"hosts", `      runtime:
        allow:
          - hosts: null
            methods: [GET]
`, "declares no hosts"},
	} {
		t.Run(test.name+" null", func(t *testing.T) {
			_, err := Validate(policyV2(t, test.config))
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestNetworkEntryShape(t *testing.T) {
	_, err := Validate(policyV2(t, `      runtime:
        allow:
          - methods: [GET]
`))
	require.ErrorContains(t, err, "declares no hosts")

	_, err = Validate(policyV2(t, `      runtime:
        allow:
          - hosts: [api.example.com]
            methods: [GET]
            paths: [v1/**]
`))
	require.ErrorContains(t, err, `must start with "/"`)
}

// A pattern cannot be bounded and unbounded at once: whatever an entry
// bounds, an overlapping pattern would grant outright, and ranking the two
// needs the matcher this package deliberately does not own. The constraint
// is per-entry, so a bare entry beside a bounded one keeps @1's patterns.
func TestNetworkEntryBoundingRequiresLiteralHosts(t *testing.T) {
	for _, test := range []struct{ name, config, wantErr string }{
		{
			name: "a bounded allow entry names hosts exactly",
			config: `      runtime:
        allow:
          - hosts: ["*.example.com"]
            methods: [GET]
`,
			wantErr: `bounds the pattern "*.example.com"`,
		},
		{
			name: "a bare entry beside a bounded one may still be a pattern",
			config: `      runtime:
        allow:
          - "**"
          - hosts: [api.example.com]
            methods: [GET]
`,
		},
		{
			name: "a bounded deny entry may name a pattern",
			config: `      runtime:
        allow: ["**"]
        deny:
          - hosts: ["*.example.com"]
            methods: [DELETE]
`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Validate(policyV2(t, test.config))
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

// The two versions describe one grant, so a descriptor states one.
func TestNetworkPolicyVersionsAreExclusive(t *testing.T) {
	d, err := Decode([]byte(`schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime: {allow: [api.example.com]}
  - type: com.docker.sandbox/network-policy@2
    config:
      runtime: {allow: [api.example.com]}
`))
	require.NoError(t, err)
	_, err = Validate(d)
	require.ErrorContains(t, err, "states one network-policy version")

	// Each is still a singleton in its own right.
	d, err = Decode([]byte(`schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/network-policy@2
    config:
      runtime: {allow: [a.example.com]}
  - type: com.docker.sandbox/network-policy@2
    config:
      runtime: {allow: [b.example.com]}
`))
	require.NoError(t, err)
	_, err = Validate(d)
	require.ErrorContains(t, err, "already declared")
}

// Bounding is @2's addition; under @1 an entry object is not a host
// string, so a kit cannot bound anything without moving version.
func TestNetworkPolicyV1RejectsEntryObjects(t *testing.T) {
	d, err := Decode([]byte(`schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow:
          - hosts: [api.example.com]
            methods: [GET]
`))
	require.NoError(t, err)
	_, err = Validate(d)
	require.ErrorContains(t, err, "cannot unmarshal object")
}

// Consumers read one shape whichever version the kit declared.
func TestNetworkPolicyV2OfLiftsV1(t *testing.T) {
	d, err := Decode([]byte(`schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      install: {allow: [registry.npmjs.org]}
      runtime: {allow: [api.example.com], deny: [telemetry.example.com]}
`))
	require.NoError(t, err)

	p, err := NetworkPolicyV2Of(d.Capabilities)
	require.NoError(t, err)
	require.Equal(t, []NetworkEntry{{Hosts: []string{"registry.npmjs.org"}}}, p.Install.Allow)
	require.Equal(t, []NetworkEntry{{Hosts: []string{"api.example.com"}}}, p.Runtime.Allow)
	require.Equal(t, []NetworkEntry{{Hosts: []string{"telemetry.example.com"}}}, p.Runtime.Deny)
	for _, e := range p.Runtime.Allow {
		require.False(t, e.Bounded(), "an @1 policy bounds no method or path")
	}

	// The @1-only accessor keeps its meaning: a @2 descriptor reads as no
	// @1 entry rather than as a lifted one.
	v2 := policyV2(t, "      runtime:\n        allow: [api.example.com]\n")
	one, err := NetworkPolicyOf(v2.Capabilities)
	require.NoError(t, err)
	require.Nil(t, one)
}

// The inject⊆allow invariant is version-agnostic, and a bounded host is
// still a host the phase reaches.
func TestNetworkPolicyV2KeepsInjectWithinAllow(t *testing.T) {
	descriptor := func(domain string) *Descriptor {
		d, err := Decode([]byte(`schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/network-policy@2
    config:
      runtime:
        allow:
          - hosts: [api.example.com]
            methods: [GET]
  - type: com.docker.sandbox/credential@1
    config:
      service: example
      phase: runtime
      apiKey:
        name: EXAMPLE_API_KEY
        inject:
          - {domain: ` + domain + `, header: x-api-key}
`))
		require.NoError(t, err)
		return d
	}

	_, err := Validate(descriptor("elsewhere.example.com"))
	require.ErrorContains(t, err, "runtime allow list")

	_, err = Validate(descriptor("api.example.com"))
	require.NoError(t, err, "a bounded host is still a host the phase reaches")
}

func TestNetworkPolicyV2SurfaceAndGate(t *testing.T) {
	granted := SurfaceOf(policyV2(t, `      runtime:
        allow:
          - plain.example.com
          - hosts: [api.example.com]
            methods: [GET, HEAD]
            paths: [/repos/**]
        deny:
          - hosts: [api.example.com]
            methods: [DELETE]
`))

	// Only the bare entry grants the connection; the bounded one grants
	// requests, so its host is not in the connection list.
	require.Equal(t, []string{"plain.example.com"}, granted.NetworkRuntimeAllow)

	// One atomic grant per method, host, and path, with a bare entry
	// rendering as the wildcard it is.
	require.Equal(t, []string{
		"ANY plain.example.com/**",
		"GET api.example.com/repos/**",
		"HEAD api.example.com/repos/**",
	}, granted.NetworkRuntimeHTTPAllow)
	require.Equal(t, []string{"DELETE api.example.com/**"}, granted.NetworkRuntimeHTTPDeny)
	require.Empty(t, granted.Services, "a decodable @2 policy is not also a service entry")

	// Adding a path to an existing entry names requests that were refused.
	widerPath := SurfaceOf(policyV2(t, `      runtime:
        allow:
          - plain.example.com
          - hosts: [api.example.com]
            methods: [GET, HEAD]
            paths: [/repos/**, /user]
        deny:
          - hosts: [api.example.com]
            methods: [DELETE]
`))
	ws := DiffWidenings(granted, widerPath)
	require.Len(t, ws, 2)
	require.Equal(t, "network.runtime.http.allow", ws[0].Category)
	require.Equal(t, []string{"GET api.example.com/user", "HEAD api.example.com/user"},
		[]string{ws[0].Detail, ws[1].Detail})

	// Dropping the deny widens, as dropping a host deny does.
	noDeny := SurfaceOf(policyV2(t, `      runtime:
        allow:
          - plain.example.com
          - hosts: [api.example.com]
            methods: [GET, HEAD]
            paths: [/repos/**]
`))
	ws = DiffWidenings(granted, noDeny)
	require.Len(t, ws, 1)
	require.Equal(t, "network.runtime.http.deny-removed", ws[0].Category)

	// An @1 policy and the @2 that lifts it are the same grant.
	v1 := SurfaceOf(mustDecode(t, `schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime: {allow: [api.example.com]}
`))
	v2 := SurfaceOf(policyV2(t, "      runtime:\n        allow: [api.example.com]\n"))
	require.Empty(t, DiffWidenings(v1, v2))
	require.Empty(t, DiffWidenings(v2, v1))
}

// Tightening is not widening. Rendering an entry whole would make every
// one of these read as a new grant, because the string changed.
func TestNetworkPolicyV2NarrowingIsNotWidening(t *testing.T) {
	surface := func(entries string) Surface {
		return SurfaceOf(policyV2(t, "      runtime:\n        allow:\n"+entries))
	}

	granted := surface(`          - hosts: [api.example.com]
            methods: [GET, HEAD]
`)
	fewerMethods := surface(`          - hosts: [api.example.com]
            methods: [GET]
`)
	require.Empty(t, DiffWidenings(granted, fewerMethods), "dropping a method gives a grant up")

	// Bounding a host that was unbounded is a narrowing; dropping the
	// bound is the widening, because a bare entry renders as ANY.
	unbounded := surface("          - api.example.com\n")
	require.Empty(t, DiffWidenings(unbounded, granted), "bounding an unrestricted host narrows it")
	require.NotEmpty(t, DiffWidenings(granted, unbounded), "dropping the bound widens it")
}

// A bare deny projects twice — into the host list and as ANY host/** —
// because an unbounded entry refuses every request as well as the
// connection. Dropping it is one loss, so it reports once. A bounded deny
// has no host-list counterpart and reports as it always did.
func TestNetworkPolicyV2DenyRemovalReportsOnce(t *testing.T) {
	categories := func(ws []Widening) []string {
		var out []string
		for _, w := range ws {
			out = append(out, w.Category)
		}
		return out
	}
	dropDeny := func(granted string) []Widening {
		return DiffWidenings(
			SurfaceOf(policyV2(t, "      runtime:\n        allow: [api.example.com]\n"+granted)),
			SurfaceOf(policyV2(t, "      runtime:\n        allow: [api.example.com]\n")),
		)
	}

	bare := dropDeny("        deny: [telemetry.example.com]\n")
	require.Equal(t, []string{"network.runtime.deny-removed"}, categories(bare),
		"the host list already reports the loss; repeating it per method says it twice")

	bounded := dropDeny(`        deny:
          - hosts: [api.example.com]
            methods: [DELETE]
`)
	require.Equal(t, []string{"network.runtime.http.deny-removed"}, categories(bounded),
		"a bounded deny has no host-list counterpart, so it reports here")
}

// A bare pattern beside a bounded entry is valid now that the literal-host
// bound is per-entry, so the gate has to read what the pattern already
// granted. The universal patterns cover every host; narrower globs are not
// expanded, and fail safe by reading as a widening rather than as silent
// coverage.
func TestNetworkPolicyV2PatternCoverage(t *testing.T) {
	surface := func(entries string) Surface {
		return SurfaceOf(policyV2(t, "      runtime:\n        allow:\n"+entries))
	}
	bounded := `          - hosts: [api.example.com]
            methods: [GET]
`

	for _, pattern := range []string{`"**"`, `"*"`} {
		granted := surface("          - " + pattern + "\n")
		candidate := surface("          - " + pattern + "\n" + bounded)
		require.Empty(t, DiffWidenings(granted, candidate),
			"%s already grants every host, so bounding one inside it adds nothing", pattern)
	}

	// A narrower glob is the runtime's matcher to decide, so the gate
	// prompts rather than assuming coverage it cannot compute.
	granted := surface(`          - "*.example.com"` + "\n")
	candidate := surface(`          - "*.example.com"` + "\n" + bounded)
	require.NotEmpty(t, DiffWidenings(granted, candidate),
		"an unexpanded glob fails safe: the gate over-prompts rather than granting silently")
}

func mustDecode(t *testing.T, yaml string) *Descriptor {
	t.Helper()
	d, err := Decode([]byte(yaml))
	require.NoError(t, err)
	return d
}

func TestResponseFieldsCarryTheRefreshMapping(t *testing.T) {
	const kit = `# syntax=docker/sandbox-kit:3
schemaVersion: "3"
displayName: Refresh Mapping
description: OAuth with nonstandard token-response field names
kind: mixin
version: "0.1.0"
provides: [refresh-mapping]

capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow:
          - api.example.com:443
  - type: com.docker.sandbox/credential@1
    config:
      service: example
      phase: runtime
      apiKey:
        name: EXAMPLE_API_KEY
        inject:
          - {domain: api.example.com, header: Authorization, format: "Bearer %s"}
      oauth:
        tokenEndpoint: {host: api.example.com, path: /token}
        sentinels: {accessToken: ex-oat-proxy-managed, refreshToken: ex-ort-proxy-managed}
        responseFields:
          accessToken: accessToken
          refreshToken: refreshToken
`
	d, err := Decode([]byte(kit))
	require.NoError(t, err)

	published, err := json.Marshal(d)
	require.NoError(t, err)

	rt, err := Decode(published)
	require.NoError(t, err)
	require.Equal(t, d, rt)

	_, err = ValidateRaw(published, rt)
	require.NoError(t, err)

	creds, err := CredentialsOf(rt.Capabilities)
	require.NoError(t, err)
	require.Len(t, creds, 1)
	rf := creds[0].OAuth.ResponseFields
	require.NotNil(t, rf)
	require.Equal(t, "accessToken", rf.AccessToken)
	require.Equal(t, "refreshToken", rf.RefreshToken)
}

func TestCredentialFileFormatSelectsTheEncoding(t *testing.T) {
	const kit = `# syntax=docker/sandbox-kit:3
schemaVersion: "3"
displayName: TOML Credential File
description: OAuth credential rendered as TOML
kind: mixin
version: "0.1.0"
provides: [toml-credential-file]

capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow:
          - api.example.com:443
  - type: com.docker.sandbox/credential@1
    config:
      service: example
      phase: runtime
      apiKey:
        name: EXAMPLE_API_KEY
        inject:
          - {domain: api.example.com, header: Authorization, format: "Bearer %s"}
      oauth:
        tokenEndpoint: {host: api.example.com, path: /token}
        credentialFile:
          path: "~/.config/example/credentials.toml"
          format: FORMAT
          structure:
            api_key: "{{.PrimaryApiKey}}"
`
	for _, format := range []string{"json", "toml"} {
		desc, err := Decode([]byte(strings.ReplaceAll(kit, "FORMAT", format)))
		require.NoError(t, err)
		_, err = Validate(desc)
		require.NoError(t, err, format)
		creds, err := CredentialsOf(desc.Capabilities)
		require.NoError(t, err)
		require.Equal(t, format, creds[0].OAuth.CredentialFile.Format)
	}

	desc, err := Decode([]byte(strings.ReplaceAll(kit, "FORMAT", "ini")))
	require.NoError(t, err)
	_, err = Validate(desc)
	require.ErrorContains(t, err, `credentialFile.format "ini" is not json or toml`)

	// An explicitly empty format decays to the omitted spelling in Go but
	// fails the schema enum; the validator rejects it from the raw shape.
	desc, err = Decode([]byte(strings.ReplaceAll(kit, `format: FORMAT`, `format: ""`)))
	require.NoError(t, err)
	_, err = Validate(desc)
	require.ErrorContains(t, err, "credentialFile.format is empty")

	// An omitted structure is schema-legal; format: toml with nothing to
	// render validates.
	noStructure := strings.ReplaceAll(kit, `format: FORMAT
          structure:
            api_key: "{{.PrimaryApiKey}}"`, `format: toml`)
	desc, err = Decode([]byte(noStructure))
	require.NoError(t, err)
	_, err = Validate(desc)
	require.NoError(t, err)

	// Present nulls are not the omitted spelling: the schema rejects
	// them, and the validator must not diverge.
	desc, err = Decode([]byte(strings.ReplaceAll(kit, "format: FORMAT", "format: null")))
	require.NoError(t, err)
	_, err = Validate(desc)
	require.ErrorContains(t, err, "credentialFile.format is null")

	nullStructure := strings.ReplaceAll(kit, `format: FORMAT
          structure:
            api_key: "{{.PrimaryApiKey}}"`, `format: toml
          structure: null`)
	desc, err = Decode([]byte(nullStructure))
	require.NoError(t, err)
	_, err = Validate(desc)
	require.ErrorContains(t, err, "credentialFile.structure is null")

	// The rest of the null family, judged by the config-wide walk: a
	// null credentialFile and a null responseFields.refreshToken both
	// decay to omitted-looking zero values in Go while the schema
	// rejects them.
	desc, err = Decode([]byte(strings.ReplaceAll(kit, `credentialFile:
          path: "~/.config/example/credentials.toml"
          format: FORMAT
          structure:
            api_key: "{{.PrimaryApiKey}}"`, `credentialFile: null`)))
	require.NoError(t, err)
	_, err = Validate(desc)
	require.ErrorContains(t, err, "oauth.credentialFile is null")

	desc, err = Decode([]byte(strings.Replace(strings.ReplaceAll(kit, "FORMAT", "json"),
		"tokenEndpoint: {host: api.example.com, path: /token}",
		`tokenEndpoint: {host: api.example.com, path: /token}
        responseFields: {accessToken: token, refreshToken: null}`, 1)))
	require.NoError(t, err)
	_, err = Validate(desc)
	require.ErrorContains(t, err, "oauth.responseFields.refreshToken is null")
}

func TestTOMLCredentialFilesRejectNullValues(t *testing.T) {
	const kit = `# syntax=docker/sandbox-kit:3
schemaVersion: "3"
displayName: Null In TOML
description: A null structure value under the toml encoding
kind: mixin
version: "0.1.0"
provides: [null-in-toml]

capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow:
          - api.example.com:443
  - type: com.docker.sandbox/credential@1
    config:
      service: example
      phase: runtime
      apiKey:
        name: EXAMPLE_API_KEY
        inject:
          - {domain: api.example.com, header: Authorization, format: "Bearer %s"}
      oauth:
        tokenEndpoint: {host: api.example.com, path: /token}
        credentialFile:
          path: "~/.config/example/credentials.toml"
          format: FORMAT
          structure:
            api_key: "{{.PrimaryApiKey}}"
            nested:
              endpoint: null
`
	desc, err := Decode([]byte(strings.ReplaceAll(kit, "FORMAT", "toml")))
	require.NoError(t, err)
	_, err = Validate(desc)
	require.ErrorContains(t, err, "structure.nested.endpoint is null, which TOML cannot represent")

	// The same structure is fine as JSON, which spells null natively.
	desc, err = Decode([]byte(strings.ReplaceAll(kit, "FORMAT", "json")))
	require.NoError(t, err)
	_, err = Validate(desc)
	require.NoError(t, err)
}

func TestAnInjectOnlyCredentialNeedsNoEnvName(t *testing.T) {
	const kit = `# syntax=docker/sandbox-kit:3
schemaVersion: "3"
displayName: Inject Only
description: A credential with no environment presence
kind: mixin
version: "0.1.0"
provides: [inject-only]

capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow:
          - api.example.com:443
  - type: com.docker.sandbox/credential@1
    config:
      service: example
      phase: runtime
      apiKey:
        inject:
          - {domain: api.example.com, header: Authorization, format: "Bearer %s"}
`
	desc, err := Decode([]byte(kit))
	require.NoError(t, err)
	_, err = Validate(desc)
	require.NoError(t, err)

	bare := strings.Replace(kit, `        inject:
          - {domain: api.example.com, header: Authorization, format: "Bearer %s"}`, `        proxyManaged: true`, 1)
	desc, err = Decode([]byte(bare))
	require.NoError(t, err)
	_, err = Validate(desc)
	require.ErrorContains(t, err, "apiKey needs a name, inject rules, or both")

	// An explicit null is not the inject-only spelling: it decays to ""
	// through the JSON round trip, but the schema requires a string, and
	// the validator must not diverge from it.
	null := strings.Replace(kit, "      apiKey:\n", "      apiKey:\n        name: null\n", 1)
	desc, err = Decode([]byte(null))
	require.NoError(t, err)
	_, err = Validate(desc)
	require.ErrorContains(t, err, "apiKey.name is null")

	// A present-null alternative decays to a nil pointer and would ride
	// beside a valid sibling; the schema requires an object.
	nullOAuth := strings.Replace(kit, `      apiKey:`, "      oauth: null\n      apiKey:", 1)
	desc, err = Decode([]byte(nullOAuth))
	require.NoError(t, err)
	_, err = Validate(desc)
	require.ErrorContains(t, err, "oauth is null; omit the field or declare a value")
}
