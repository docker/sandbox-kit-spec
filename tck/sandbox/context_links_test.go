package sandbox

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type contextLinkCase struct {
	name       string
	index      string
	target     string
	linked     bool
	attributed bool
}

func contextLinkCases() []contextLinkCase {
	return []contextLinkCase{
		{"angle-with-spaces", "- [context-v2-tool](<kits/tool guidance.md>)", "kits/tool guidance.md", true, true},
		{"forward-reference", "- [context-v2-tool][tool]\n\n[tool]: <kits/tool guidance.md>", "kits/tool guidance.md", true, true},
		{"backward-reference", "[ Tool  Guide ]: ./tool.md\n\n- [context-v2-tool][tool guide]", "tool.md", true, true},
		{"collapsed-reference", "- [context-v2-tool][]\n\n[context-v2-tool]: ./tool.md", "tool.md", true, true},
		{"shortcut-reference", "- [context-v2-tool]\n\n[context-v2-tool]: ./tool.md", "tool.md", true, true},
		{"continued-definition", "- [context-v2-tool][tool]\n\n[tool]:\n  <kits/tool guidance.md>", "kits/tool guidance.md", true, true},
		{"link-title", "- [context-v2-tool](<kits/tool guidance.md> \"Read the guidance\")", "kits/tool guidance.md", true, true},
		{"balanced-parentheses", "- [context-v2-tool](./tool(version(2)).md)", "tool(version(2)).md", true, true},
		{"percent-encoding", "- [context-v2-tool](./tool%20guidance.md)", "tool guidance.md", true, true},
		{"character-reference", "- [context-v2-tool](<tool &amp; guidance.md>)", "tool & guidance.md", true, true},
		{"escaped-punctuation", "- [context-v2-tool](./tool\\[2\\].md)", "tool[2].md", true, true},
		{"absolute-path", "- **context-v2-tool**: $ABS", "tool.md", true, true},
		{"code-path-with-spaces", "- **context-v2-tool**: `kits/tool guidance.md`", "kits/tool guidance.md", true, true},
		{"code-attribution", "- `context-v2-tool`: [Read guidance](./tool.md)", "tool.md", true, true},
		{"versioned-code-attribution", "- `context-v2-tool@1.0.0`: [Read guidance](./tool.md)", "tool.md", true, true},
		{"versioned-code-heading", "# `context-v2-tool@1.0.0-beta.1+build.2`\n\n[Read guidance](./tool.md)", "tool.md", true, true},
		{"code-path-is-not-attribution", "- `./context-v2-tool@1.0.0.md`: [Read guidance](./tool.md)", "tool.md", true, false},
		{"unmatched-outer-bracket", "unmatched [ before [context-v2-tool](./tool.md)", "tool.md", true, true},
		{"invalid-definition-before-link", "[unused]:\n- [context-v2-tool](./tool.md)", "tool.md", true, true},
		{"first-definition-wins", "- [context-v2-tool][tool]\n\n[tool]: ./tool.md\n[tool]: ./missing.md", "tool.md", true, true},
		{"kit-heading", "# context-v2-tool\n\n[Read guidance](./tool.md)", "tool.md", true, true},
		{"definition-is-not-attribution", "- [Read guidance][context-v2-tool]\n\n[context-v2-tool]: ./tool.md", "tool.md", true, false},
		{"title-is-not-attribution", "- [Read guidance][tool]\n\n[tool]: ./tool.md \"context-v2-tool\"", "tool.md", true, false},
		{"path-is-not-attribution", "- [Read guidance](./context-v2-tool.md)", "context-v2-tool.md", true, false},
		{"heading-path-is-not-attribution", "# ./context-v2-tool.md\n\n[Read guidance](./tool.md)", "tool.md", true, false},
		{"unused-definition", "[context-v2-tool]: ./tool.md", "tool.md", false, false},
		{"unresolved-reference", "- [context-v2-tool][missing]", "tool.md", false, false},
		{"remote-link", "- [context-v2-tool](https://example.test/tool.md)", "tool.md", false, false},
	}
}

func contextLinkFixture(t *testing.T, tc contextLinkCase) (profile, text, target string) {
	t.Helper()
	directory := t.TempDir()
	profile = filepath.Join(directory, "AGENTS.md")
	target = filepath.Join(directory, tc.target)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700))
	require.NoError(t, os.WriteFile(target, []byte("expanded progressive body"), 0o600))
	text = strings.ReplaceAll(tc.index, "$ABS", target)
	return profile, text, target
}

func TestContextLinksRecognizeAttributedDestinations(t *testing.T) {
	for _, tc := range contextLinkCases() {
		t.Run(tc.name, func(t *testing.T) {
			profile, text, target := contextLinkFixture(t, tc)
			got := indexedContextBody(profile, text, "context-v2-tool", "expanded progressive body", func(candidate string) (string, bool) {
				body, err := os.ReadFile(candidate)
				return string(body), err == nil
			})
			if tc.attributed {
				require.Equal(t, target, got)
			} else {
				require.Empty(t, got)
			}
		})
	}
}

// The observer is compiled separately in the workload image. Exercise its
// real executable against the same destinations, including paths with spaces,
// so accepting a link here cannot hide missing startup evidence.
func TestContextLinksStartupObserverParity(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "snapshot")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "testdata/fixtures/context-v2-workload/snapshot.go")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "%s", output)
	outputFile := filepath.Join(t.TempDir(), "observed.json")
	arguments := []string{outputFile}
	want := map[string]map[string]string{}
	parsed := map[string]map[string]string{}
	for _, tc := range contextLinkCases() {
		profile, text, target := contextLinkFixture(t, tc)
		snapshot := filepath.Join(filepath.Dir(profile), "at-launch")
		require.NoError(t, os.WriteFile(snapshot, []byte(text), 0o600))
		arguments = append(arguments, profile, snapshot)
		want[profile] = map[string]string{}
		if tc.linked {
			want[profile][target] = "expanded progressive body"
		}
		parsed[profile] = map[string]string{}
		for _, link := range contextLinks(text) {
			candidate := link.destination
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(filepath.Dir(profile), candidate)
			}
			if body, err := os.ReadFile(candidate); err == nil {
				parsed[profile][candidate] = string(body)
			}
		}
	}
	output, err = exec.CommandContext(t.Context(), binary, arguments...).CombinedOutput()
	require.NoError(t, err, "%s", output)
	raw, err := os.ReadFile(outputFile)
	require.NoError(t, err)
	var observed map[string]map[string]string
	require.NoError(t, json.Unmarshal(raw, &observed))
	require.Equal(t, want, parsed, "the conformance parser resolves complete local destinations")
	require.Equal(t, want, observed, "the standalone startup observer resolves the same destinations")
}
