package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		{"repeated-reference-backtick-label", "# context-v2-tool\n\n[x`]: ./ref.md\n\n[x`] first\n\n[x`] `docs/b.md`", "docs/b.md", true, true},
		{"repeated-reference-path-label-is-hidden", "[r]: ./ref.md\n\n[./label/path][r]\n\n[./label/path][r]", "label/path", false, false},
		{"escaped-code-opener-preserves-link", "\\`[context-v2-tool](./tool.md)`", "tool.md", true, true},
		{"code-span-link-is-literal", "`[context-v2-tool](./tool.md)`", "tool.md", false, false},
		{"code-span-reference-is-literal", "`[context-v2-tool][tool]`\n\n[tool]: ./tool.md", "tool.md", false, false},
		{"code-span-link-before-real-link", "`[Other](./missing.md)` context-v2-tool: [Read guidance](./tool.md)", "tool.md", true, true},
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
		{"qualified-code-attribution", "- `docker.io/library/context-v2-tool:1.0.0`: [Read guidance](./tool.md)", "tool.md", true, true},
		{"qualified-code-heading", "# `docker.io/library/context-v2-tool:1.0.0`\n## Guidance\n[Read guidance](./tool.md)", "tool.md", true, true},
		{"qualified-code-port-attribution", "- `registry.example.test:5000/team/context-v2-tool:latest`: [Read guidance](./tool.md)", "tool.md", true, true},
		{"qualified-destination-is-not-attribution", "- [Other](docker.io/library/context-v2-tool:1.0.0) [Read guidance](./tool.md)", "tool.md", true, false},
		{"qualified-reference-is-not-attribution", "- [Read guidance][tool]\n\n[tool]: ./tool.md \"docker.io/library/context-v2-tool:1.0.0\"", "tool.md", true, false},
		{"absolute-qualified-code-path-is-not-attribution", "- `/home/agent/docker.io/library/context-v2-tool:1.0.0`: [Read guidance](./tool.md)", "tool.md", true, false},
		{"relative-qualified-code-path-is-not-attribution", "- `./docker.io/library/context-v2-tool:1.0.0`: [Read guidance](./tool.md)", "tool.md", true, false},
		{"code-absolute-fragment-literal", "- context-v2-tool: `$ABS`", "kits/tool#1.md", true, true},
		{"code-relative-fragment-literal", "- context-v2-tool: `./tool#1.md`", "tool#1.md", true, true},
		{"code-percent-escape-literal", "- context-v2-tool: `./tool%20guidance.md`", "tool%20guidance.md", true, true},
		{"code-invalid-percent-escape-literal", "- context-v2-tool: `./tool%name.md`", "tool%name.md", true, true},
		{"code-query-literal", "- context-v2-tool: `./tool?1.md`", "tool?1.md", true, true},
		{"code-character-reference-literal", "- context-v2-tool: `./tool&amp;guide.md`", "tool&amp;guide.md", true, true},
		{"code-backslash-literal", "- context-v2-tool: `./tool\\[1].md`", "tool\\[1].md", true, true},
		{"plain-fragment-literal", "- context-v2-tool: ./tool#1.md", "tool#1.md", true, true},
		{"plain-percent-escape-literal", "- context-v2-tool: ./tool%20guidance.md", "tool%20guidance.md", true, true},
		{"link-fragment-is-not-path", "- [context-v2-tool](./tool.md#guidance)", "tool.md", true, true},
		{"link-percent-fragment-is-path", "- [context-v2-tool](./tool%231.md)", "tool#1.md", true, true},
		{"reference-percent-encoding", "- [context-v2-tool][tool]\n\n[tool]: ./tool%20guidance.md#guidance", "tool guidance.md", true, true},
		{"unmatched-outer-bracket", "unmatched [ before [context-v2-tool](./tool.md)", "tool.md", true, true},
		{"invalid-definition-before-link", "[unused]:\n- [context-v2-tool](./tool.md)", "tool.md", true, true},
		{"first-definition-wins", "- [context-v2-tool][tool]\n\n[tool]: ./tool.md\n[tool]: ./missing.md", "tool.md", true, true},
		{"kit-heading", "# context-v2-tool\n\n[Read guidance](./tool.md)", "tool.md", true, true},
		{"ancestor-kit-heading", "# context-v2-tool\n## Guidance\n[Read guidance](./tool.md)", "tool.md", true, true},
		{"multiple-ancestor-headings", "# Kits\n## context-v2-tool\n#### Guidance\n[Read guidance](./tool.md)", "tool.md", true, true},
		{"ancestor-survives-child-sibling", "# context-v2-tool\n## Overview\n### Details\n## Guidance\n[Read guidance](./tool.md)", "tool.md", true, true},
		{"same-level-heading-closes-kit", "# Kits\n## context-v2-tool\n### Guidance\n## Other Kit\n[Read guidance](./tool.md)", "tool.md", true, false},
		{"higher-level-heading-closes-kit", "## context-v2-tool\n### Guidance\n# Other Kit\n[Read guidance](./tool.md)", "tool.md", true, false},
		{"list-heading-under-section-is-not-attribution", "# Kits\n- ## context-v2-tool\n- [Read guidance](./tool.md)", "tool.md", true, false},
		{"nested-list-heading-is-not-attribution", "# Kits\n- Other Kit\n  - ## context-v2-tool\n  - [Read guidance](./tool.md)", "tool.md", true, false},
		{"heading-after-list-closes-kit", "# context-v2-tool\n- Other guidance\n# Other Kit\n## Guidance\n[Read guidance](./tool.md)", "tool.md", true, false},
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
	binary := buildContextObserver(t)
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
	output, err := exec.CommandContext(t.Context(), binary, arguments...).CombinedOutput()
	require.NoError(t, err, "%s", output)
	raw, err := os.ReadFile(outputFile)
	require.NoError(t, err)
	var observed map[string]map[string]string
	require.NoError(t, json.Unmarshal(raw, &observed))
	t.Run("scanner", func(t *testing.T) {
		require.Equal(t, want, parsed, "the conformance parser resolves complete local destinations")
	})
	t.Run("executable", func(t *testing.T) {
		require.Equal(t, want, observed, "the standalone startup observer resolves the same destinations")
	})
}

func buildContextObserver(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "snapshot")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "testdata/fixtures/context-v2-workload/snapshot.go")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "%s", output)
	return binary
}

func runContextObserver(t *testing.T, binary string, arguments ...string) map[string]map[string]string {
	t.Helper()
	outputFile := filepath.Join(t.TempDir(), "observed.json")
	// A FIFO with no writer used to block launch indefinitely. Bound the
	// executable, rather than leaving a blocked goroutine in the test runner.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, append([]string{outputFile}, arguments...)...).CombinedOutput()
	require.NoError(t, ctx.Err(), "startup observer blocked: %s", output)
	require.NoError(t, err, "%s", output)
	raw, err := os.ReadFile(outputFile)
	require.NoError(t, err)
	var observed map[string]map[string]string
	require.NoError(t, json.Unmarshal(raw, &observed))
	return observed
}

func TestContextLinksStartupObserverBounds(t *testing.T) {
	binary := buildContextObserver(t)
	const fileLimit = 1 << 20
	for _, kind := range []string{"fifo", "symlink-fifo", "oversized", "directory", "device"} {
		t.Run("target-"+kind, func(t *testing.T) {
			dir := t.TempDir()
			bad := filepath.Join(dir, "bad")
			switch kind {
			case "fifo", "symlink-fifo":
				output, err := exec.CommandContext(t.Context(), "mkfifo", bad).CombinedOutput()
				require.NoError(t, err, "%s", output)
				if kind == "symlink-fifo" {
					require.NoError(t, os.Symlink(bad, bad+"-link"))
					bad += "-link"
				}
			case "oversized":
				// Small enough to reproduce safely on the old executable.
				require.NoError(t, os.WriteFile(bad, []byte(strings.Repeat("x", fileLimit+1)), 0o600))
			case "directory":
				require.NoError(t, os.Mkdir(bad, 0o700))
			case "device":
				// /dev/null is safe even if regular-file rejection regresses.
				require.NoError(t, os.Symlink("/dev/null", bad))
			}
			good := filepath.Join(dir, "good")
			require.NoError(t, os.WriteFile(good, []byte("complete guidance"), 0o600))
			profile, snapshot := filepath.Join(dir, "AGENTS.md"), filepath.Join(dir, "snapshot")
			require.NoError(t, os.WriteFile(snapshot, []byte(fmt.Sprintf("[bad](%s)\n\n[good](%s)", bad, good)), 0o600))
			observed := runContextObserver(t, binary, profile, snapshot)
			require.Equal(t, map[string]map[string]string{profile: {good: "complete guidance"}}, observed)
		})
	}
	for _, kind := range []string{"fifo", "oversized"} {
		t.Run("snapshot-"+kind, func(t *testing.T) {
			dir := t.TempDir()
			bad := filepath.Join(dir, "bad")
			if kind == "fifo" {
				output, err := exec.CommandContext(t.Context(), "mkfifo", bad).CombinedOutput()
				require.NoError(t, err, "%s", output)
			} else {
				require.NoError(t, os.WriteFile(bad, []byte(strings.Repeat("x", fileLimit+1)), 0o600))
			}
			good := filepath.Join(dir, "good")
			require.NoError(t, os.WriteFile(good, []byte("guidance"), 0o600))
			snapshot, profile := filepath.Join(dir, "snapshot"), filepath.Join(dir, "AGENTS.md")
			require.NoError(t, os.WriteFile(snapshot, []byte("[good](./good)"), 0o600))
			observed := runContextObserver(t, binary, filepath.Join(dir, "BAD.md"), bad, profile, snapshot)
			require.Equal(t, map[string]map[string]string{profile: {good: "guidance"}}, observed)
		})
	}
}

func TestContextLinksStartupObserverBudget(t *testing.T) {
	binary := buildContextObserver(t)
	t.Run("three-profiles-with-repeated-links", func(t *testing.T) {
		dir := t.TempDir()
		// A full descriptor budget per snapshot and per body, with worst
		// case JSON escaping, still leaves headroom for all three profiles.
		body := strings.Repeat("\x00", 512<<10)
		target := filepath.Join(dir, "body")
		require.NoError(t, os.WriteFile(target, []byte(body), 0o600))
		linked := filepath.Join(dir, "linked")
		require.NoError(t, os.Symlink(target, linked))
		index := strings.Repeat("[context-v2-tool](./linked)\n\n", 64)
		index = strings.Repeat("x", (512<<10)-len(index)-2) + "\n\n" + index
		snapshot := filepath.Join(dir, "snapshot")
		require.NoError(t, os.WriteFile(snapshot, []byte(index), 0o600))
		require.NoError(t, os.Symlink(snapshot, snapshot+"-link"))
		var arguments []string
		for i := range 3 {
			profile := filepath.Join(dir, fmt.Sprintf("PROFILE-%d", i))
			// Repeating a profile must not spend the allowance again either.
			arguments = append(arguments, profile, snapshot+"-link", profile, snapshot+"-link")
		}
		observed := runContextObserver(t, binary, arguments...)
		require.Len(t, observed, 3)
		for _, files := range observed {
			require.Len(t, files, 1)
			require.Equal(t, body, files[linked])
		}
	})
	t.Run("aggregate-skips-whole-bodies-and-continues", func(t *testing.T) {
		dir := t.TempDir()
		body := strings.Repeat("x", 512<<10)
		target := filepath.Join(dir, "body")
		require.NoError(t, os.WriteFile(target, []byte(body), 0o600))
		var index strings.Builder
		for i := range 12 {
			name := fmt.Sprintf("body-%d", i)
			require.NoError(t, os.Symlink(target, filepath.Join(dir, name)))
			fmt.Fprintf(&index, "[context-v2-tool](./%s)\n\n", name)
		}
		good := filepath.Join(dir, "good")
		require.NoError(t, os.WriteFile(good, []byte("remaining valid guidance"), 0o600))
		index.WriteString("[context-v2-tool](./good)")
		profile, snapshot := filepath.Join(dir, "AGENTS.md"), filepath.Join(dir, "snapshot")
		require.NoError(t, os.WriteFile(snapshot, []byte(index.String()), 0o600))
		observed := runContextObserver(t, binary, profile, snapshot)
		files := observed[profile]
		// Each half-MiB body costs 3.5 MiB including its JSON allowance;
		// four fit in 16 MiB. The next eight are omitted in their entirety.
		require.Len(t, files, 5)
		for i := range 12 {
			got, ok := files[filepath.Join(dir, fmt.Sprintf("body-%d", i))]
			if i < 4 {
				require.True(t, ok)
				require.Equal(t, body, got)
			} else {
				require.False(t, ok, "an over-budget body must not become truncated evidence")
			}
		}
		require.Equal(t, "remaining valid guidance", files[good])
	})

	t.Run("per-file-limit-is-inclusive", func(t *testing.T) {
		dir := t.TempDir()
		body := strings.Repeat("x", 1<<20)
		target := filepath.Join(dir, "body")
		require.NoError(t, os.WriteFile(target, []byte(body), 0o600))
		index := "[context-v2-tool](./body)"
		index = strings.Repeat("x", (1<<20)-len(index)-2) + "\n\n" + index
		profile, snapshot := filepath.Join(dir, "AGENTS.md"), filepath.Join(dir, "snapshot")
		require.NoError(t, os.WriteFile(snapshot, []byte(index), 0o600))
		observed := runContextObserver(t, binary, profile, snapshot)
		require.Equal(t, body, observed[profile][target])
	})
}

func TestContextLinksStartupObserverAvoidsAttributionAmplification(t *testing.T) {
	limiter, err := exec.LookPath("prlimit")
	if err != nil {
		t.Skip("prlimit is required for the observer memory regression")
	}
	binary := buildContextObserver(t)
	for name, body := range map[string]string{
		"heading-attribution":       "# " + strings.Repeat("x", 256<<10) + "\n\n" + strings.Repeat("[x](./x)\n\n", 20000),
		"repeated-valid-references": "[x]: ./x\n\n" + strings.Repeat("[x]\n\n", 20000),
		"reference-destinations":    "[x]: ./" + strings.Repeat("x", 256<<10) + "\n\n" + strings.Repeat("[x]\n\n", 20000),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			profile, snapshot := filepath.Join(dir, "AGENTS.md"), filepath.Join(dir, "snapshot")
			require.NoError(t, os.WriteFile(snapshot, []byte(body), 0o600))
			target := filepath.Join(dir, "x")
			require.NoError(t, os.WriteFile(target, []byte("complete guidance"), 0o600))
			outputFile := filepath.Join(dir, "observed.json")
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, limiter, "--as=1073741824", "--", binary, outputFile, profile, snapshot)
			command.Env = append(os.Environ(), "GOMEMLIMIT=64MiB")
			output, err := command.CombinedOutput()
			require.NoError(t, ctx.Err(), "observer did not complete: %s", output)
			require.NoError(t, err, "%s", output)
			raw, err := os.ReadFile(outputFile)
			require.NoError(t, err)
			var observed map[string]map[string]string
			require.NoError(t, json.Unmarshal(raw, &observed))
			want := map[string]map[string]string{profile: {target: "complete guidance"}}
			if name == "reference-destinations" {
				want[profile] = map[string]string{}
			}
			require.Equal(t, want, observed)
		})
	}
}

func FuzzContextLinks(f *testing.F) {
	for _, tc := range contextLinkCases() {
		f.Add(tc.index)
	}
	for _, seed := range []string{"[x](file \"\\", "[x]: file\n \"\\", "[[]](../x)", "`[x](./x)`", "###### x\n[y](<../z>)"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 32768 {
			t.Skip()
		}
		contextLinks(text)
	})
}
