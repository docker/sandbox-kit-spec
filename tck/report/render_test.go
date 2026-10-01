package report

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// judged builds a report the way a suite does: every check recorded,
// findings only where a check had something to say.
func judged() Report {
	var r Report
	r.Add("descriptor-valid", "SPEC-v3 §9.3")
	r.Add("schema-version-annotation", "SPEC-v3 §9.3", Failf("annotation says %q, descriptor says %q", "2", "3"))
	r.Add("oci-annotations", "SPEC-v3 §9.3",
		Failf("title disagrees with the descriptor"),
		Failf("created must be an RFC 3339 timestamp"))
	r.Add("index-annotations", "SPEC-v3 §9.3", Warnf("index carries no descriptor annotation"))
	r.Add("staged-recipe", "SPEC-v3 §10", Skipf("no staged kit root to look in"))
	return r
}

func render(t *testing.T, r Report, o Options) string {
	t.Helper()
	var b strings.Builder
	require.NoError(t, r.Render(&b, o))
	return b.String()
}

// The tally is what a reader checks first, so it has to add up: a check
// reporting three failures is one failed check, not three.
func TestTheSummaryCountsChecksByTheirWorstFinding(t *testing.T) {
	s := judged().Summary()
	require.Equal(t, Summary{Checks: 5, Failed: 2, Warned: 1, Skipped: 1, Passed: 1}, s)
	require.Equal(t, s.Checks, s.Failed+s.Warned+s.Skipped+s.Passed)
	require.Equal(t, "5 checks · 2 failed · 1 warned · 1 skipped · 1 passed", s.String())
}

// A passing check is silent by default — a clean run's news is that
// there is none — but a reader who wants to see what ran can.
func TestPassingChecksAppearOnlyWhenAsked(t *testing.T) {
	require.NotContains(t, render(t, judged(), Options{}), "descriptor-valid")

	verbose := render(t, judged(), Options{Verbose: true})
	require.Contains(t, verbose, "descriptor-valid")
	require.Contains(t, verbose, "passed")
}

// Severities are grouped, worst first: a skip between two failures
// makes a reader sort the list themselves.
func TestFindingsAreGroupedWorstFirst(t *testing.T) {
	out := render(t, judged(), Options{})
	require.Less(t, strings.Index(out, "failed"), strings.Index(out, "warned"))
	require.Less(t, strings.Index(out, "warned"), strings.Index(out, "skipped"))
}

// Escapes belong to terminals. A redirected run is read by a pipe, a
// log, or a test failure message, and none of them want them.
func TestPlainRenderingCarriesNoEscapes(t *testing.T) {
	require.NotContains(t, render(t, judged(), Options{Verbose: true}), "\x1b")
	require.Contains(t, render(t, judged(), Options{Color: true}), "\x1b[")
}

// A requirement is a link where the terminal carries one, and the URL
// stays out of the text everywhere else.
func TestHyperlinksCarryTheSpecURL(t *testing.T) {
	link := func(string) string { return "https://example.com/spec#93" }

	linked := render(t, judged(), Options{Color: true, Hyperlinks: true, Link: link})
	require.Contains(t, linked, "\x1b]8;;https://example.com/spec#93")

	plain := render(t, judged(), Options{Link: link})
	require.NotContains(t, plain, "https://example.com/spec#93")
	require.Contains(t, plain, "[SPEC-v3 §9.3]")
}

// A runtime check is named by the requirement it judges. Printing both
// would say the same thing twice on every line of the report.
func TestARequirementThatIsTheCheckNameIsNotRepeated(t *testing.T) {
	var r Report
	r.Add("lifecycle@1/install-once", "lifecycle@1/install-once", Failf("hooks ran twice"))

	out := render(t, r, Options{})
	require.Contains(t, out, "lifecycle@1/install-once")
	require.NotContains(t, out, "[lifecycle@1/install-once]")
}

// Detail wraps into a column rather than running off the terminal, and
// what wraps lines up under what it continues.
func TestDetailWrapsUnderItsOwnColumn(t *testing.T) {
	var r Report
	r.Add("staged-sources", "SPEC-v3 §10", Failf("%s", strings.Repeat("word ", 40)))

	lines := strings.Split(strings.TrimRight(render(t, r, Options{Width: 70}), "\n"), "\n")
	require.Greater(t, len(lines), 3, "a long detail must wrap")

	// Columns, not bytes: the severity symbol is one column of several
	// bytes, which is the measurement the renderer itself has to get
	// right for anything to line up.
	var starts []int
	for _, line := range lines {
		if at := strings.Index(line, "word"); at >= 0 {
			require.LessOrEqual(t, columns(line), 70, "%q runs past the width", line)
			starts = append(starts, columns(line[:at]))
		}
	}
	require.Greater(t, len(starts), 2)
	for _, start := range starts[1:] {
		require.Equal(t, starts[0], start, "continuations line up under the detail")
	}
}

func TestSourceExcerptsKeepTheirLayout(t *testing.T) {
	var r Report
	r.Add("descriptor-valid", "SPEC-v3 §9.3", Failf("%s", "kit.yaml:5:25: invalid port\n  |\n5 |     config: {container: 99999}\n  |                         ^\n\nkit.yaml:8:14: invalid pattern\n  |\n8 |     pattern: '['\n  |              ^"))
	out := render(t, r, Options{Width: 45})
	require.Contains(t, out, "\n      5 |     config: {container: 99999}\n        |                         ^\n      \n      kit.yaml:8:14:")
	require.Contains(t, out, "\n      8 |     pattern: '['\n        |              ^")
}
