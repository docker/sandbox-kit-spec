package sandbox

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
	"github.com/stretchr/testify/require"
)

// Claim generic credentials while omitting native ChatGPT, so accepting
// generic OAuth cannot conceal a missing native support check.
func TestOpenAIChatGPTRequiredUnclaimedIsRefused(t *testing.T) {
	for _, broken := range []string{"", "accepts-required-unclaimed"} {
		t.Run(broken, func(t *testing.T) {
			claimed := make(map[string]bool)
			var claims []string
			for _, typ := range spec.KnownCapabilities() {
				if typ != capOpenAIChatGPT {
					claimed[typ] = true
					claims = append(claims, typ)
				}
			}
			a := adapter.New(filepath.Join("testdata", "fake-adapter"))
			a.Env = []string{
				"KIT_TCK_FAKE_STATE=" + t.TempDir(),
				"KIT_TCK_FAKE_CLAIMS=" + strings.Join(claims, ","),
				"KIT_TCK_FAKE_BROKEN=" + broken,
			}
			e := &Env{Adapter: a, Fixtures: Fixtures(FixtureDir), Claimed: claimed}
			for _, c := range checks {
				if c.requirement != "conformance.md §2.2/required-unclaimed-refused" {
					continue
				}
				findings := c.run(t.Context(), e)
				if broken == "" {
					require.Empty(t, findings)
				} else {
					require.Len(t, findings, 1)
					require.Equal(t, report.Fail, findings[0].Severity)
					require.Contains(t, findings[0].Detail, capOpenAIChatGPT)
				}
				return
			}
			t.Fatal("required-unclaimed refusal check is missing")
		})
	}
}
