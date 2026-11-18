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
		{"plain-parent", "- **context-v2-tool**: ../kits/tool.md", "../kits/tool.md", true, true},
		{"plain-multiple-parents", "- **context-v2-tool**: ../../kits/tool.md", "../../kits/tool.md", true, true},
		{"plain-mixed-relative-prefixes", "- **context-v2-tool**: ./.././../kits/tool.md", "../../kits/tool.md", true, true},
		{"plain-current-directory", "- **context-v2-tool**: ./kits/tool.md", "kits/tool.md", true, true},
		{"lazy-wrapped-label", "- [context-v2-tool\nguidance](./tool.md)", "tool.md", true, true},
		{"lazy-wrapped-destination", "- [context-v2-tool](\n./tool.md\n)", "tool.md", true, true},
		{"wrapped-paragraph-link", "[context-v2-tool\nguidance](\n./tool.md\n)", "tool.md", true, true},
		{"tab-continued-item", "- context-v2-tool\n\t[Read guidance](./tool.md)", "tool.md", true, true},
		{"wrapped-label", "- [context-v2-tool\n  guidance](./tool.md)", "tool.md", true, true},
		{"wrapped-destination", "- [context-v2-tool](\n  ./tool.md\n  )", "tool.md", true, true},
		{"wrapped-angle-destination", "- [context-v2-tool](\n  <kits/tool guidance.md>\n  \"Read guidance\"\n  )", "kits/tool guidance.md", true, true},
		{"wrapped-reference-label", "- [context-v2-tool\n  guidance][tool]\n\n[tool]: ./tool.md", "tool.md", true, true},
		{"continued-list-item", "- **context-v2-tool**\n  [Read guidance](./tool.md)", "tool.md", true, true},
		{"continued-code-version", "- `context-v2-tool@v1.2.3-beta.1`\n  [Read guidance](./tool.md)", "tool.md", true, true},
		{"continued-ordered-item", "10. context-v2-tool\n    [Read guidance](./tool.md)", "tool.md", true, true},
		{"continued-paragraph", "- context-v2-tool\n\n  [Read guidance](./tool.md)", "tool.md", true, true},
		{"nested-own-attribution", "- Other Kit\n  - context-v2-tool\n    [Read guidance](./tool.md)", "tool.md", true, true},
		{"parent-resumes-after-nested-item", "- context-v2-tool\n  - Other Kit\n    [Other guidance](./missing.md)\n  [Read guidance](./tool.md)", "tool.md", true, true},
		{"list-heading-cannot-lazily-continue", "- # context-v2-tool\n[Read guidance](./tool.md)", "tool.md", true, false},
		{"ordered-heading-cannot-lazily-continue", "1. ## context-v2-tool\n[Read guidance](./tool.md)", "tool.md", true, false},
		{"list-heading-is-not-document-heading", "- # context-v2-tool\n- [Read guidance](./tool.md)", "tool.md", true, false},
		{"neighbor-item-is-not-attribution", "- context-v2-tool\n- [Read guidance](./tool.md)", "tool.md", true, false},
		{"previous-item-link-is-not-attributed", "- [Read guidance](./tool.md)\n- context-v2-tool", "tool.md", true, false},
		{"continued-sibling-is-not-attribution", "- context-v2-tool\n- Other Kit\n  [Read guidance](./tool.md)", "tool.md", true, false},
		{"nested-parent-is-not-attribution", "- context-v2-tool\n  - [Read guidance](./tool.md)", "tool.md", true, false},
		{"nested-child-is-not-attribution", "- Other Kit\n  - context-v2-tool\n  [Read guidance](./tool.md)", "tool.md", true, false},
		{"nested-sibling-is-not-attribution", "- Other Kit\n  - context-v2-tool\n  - [Read guidance](./tool.md)", "tool.md", true, false},
		{"dedented-item-is-not-attribution", "- context-v2-tool\n\n[Read guidance](./tool.md)", "tool.md", true, false},
		{"neighbor-paragraph-is-not-attribution", "context-v2-tool\n\n[Read guidance](./tool.md)", "tool.md", true, false},
		{"continued-destination-is-not-attribution", "- [Other](./context-v2-tool.md)\n  [Read guidance](./tool.md)", "tool.md", true, false},
		{"continued-title-is-not-attribution", "- [Other](./missing.md \"context-v2-tool\")\n  [Read guidance](./tool.md)", "tool.md", true, false},
		{"continued-reference-id-is-not-attribution", "- [Other][context-v2-tool]\n  [Read guidance](./tool.md)\n\n[context-v2-tool]: ./missing.md", "tool.md", true, false},
		{"wrapped-title-is-not-attribution", "- [Read guidance](\n  ./tool.md\n  \"context-v2-tool\")", "tool.md", true, false},
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
		{"continued-reference-title-is-not-attribution", "- [Read guidance][tool]\n\n  [tool]: ./tool.md\n    \"context-v2-tool\"", "tool.md", true, false},
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
	directory := filepath.Join(t.TempDir(), "profiles", "nested")
	require.NoError(t, os.MkdirAll(directory, 0o700))
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
