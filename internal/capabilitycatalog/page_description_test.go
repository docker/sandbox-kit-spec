package capabilitycatalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func descriptionPage() []byte {
	return []byte("## Support by capability\n\nAll capability names in the table have the `com.docker.sandbox/` prefix.\n\n" +
		tableHeader + "\n" + tableRule + "\n" +
		"| [`alpha@2`](https://example.test/old) | v0.45.0 | Limited: v0.51.0 | Keep `detail` exactly. |\n\n" +
		"## Docs-owned guidance\nPreserve this paragraph exactly.\n")
}

func descriptionCatalog(t *testing.T) Catalog {
	t.Helper()
	c, err := Build(fixture(t), tag, rev)
	require.NoError(t, err)
	for i := range c.Capabilities {
		c.Capabilities[i].Description = "Intro for " + strings.TrimPrefix(c.Capabilities[i].ID, "com.docker.sandbox/") + "."
	}
	require.NoError(t, c.Validate())
	return c
}

func TestPageGeneratesAndRefreshesDescriptions(t *testing.T) {
	c := descriptionCatalog(t)
	page := descriptionPage()
	first, err := UpdatePage(page, c)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(first), descriptionsStart))
	require.Equal(t, 1, strings.Count(string(first), descriptionsEnd))
	require.Contains(t, string(first), "| Not verified | Not verified | Docker Sandboxes support has not been verified. |\n\n"+descriptionsStart)
	require.Less(t, strings.Index(string(first), descriptionsEnd), strings.Index(string(first), "## Docs-owned guidance"))
	require.Contains(t, string(first), "### [`alpha@2`]("+c.Capabilities[0].SpecURL+")\n\nIntro for alpha@2.")
	require.Contains(t, string(first), "### [`zeta@1`]("+c.Capabilities[1].SpecURL+")\n\nIntro for zeta@1.")
	require.Contains(t, string(first), "| v0.45.0 | Limited: v0.51.0 | Keep `detail` exactly. |")
	require.Contains(t, string(first), "| Not verified | Not verified | Docker Sandboxes support has not been verified. |")
	require.True(t, strings.HasSuffix(string(first), "## Docs-owned guidance\nPreserve this paragraph exactly.\n"))

	again, err := UpdatePage(first, c)
	require.NoError(t, err)
	require.Equal(t, first, again)

	c.Capabilities[0].Description = "Updated intro with [details](https://example.test/details)."
	updated, err := UpdatePage(first, c)
	require.NoError(t, err)
	require.Contains(t, string(updated), c.Capabilities[0].Description)
	require.NotContains(t, string(updated), "Intro for alpha@2.")
	require.Equal(t, 1, strings.Count(string(updated), descriptionsStart))
	require.Contains(t, string(updated), "| v0.45.0 | Limited: v0.51.0 | Keep `detail` exactly. |")
	require.True(t, strings.HasSuffix(string(updated), "## Docs-owned guidance\nPreserve this paragraph exactly.\n"))
}

func TestPageRejectsMalformedDescriptionMarkersWithoutWrite(t *testing.T) {
	c := descriptionCatalog(t)
	generated, err := UpdatePage(descriptionPage(), c)
	require.NoError(t, err)
	cases := map[string]string{
		"missing end":      strings.Replace(string(generated), descriptionsEnd, "", 1),
		"missing start":    strings.Replace(string(generated), descriptionsStart, "", 1),
		"duplicate start":  strings.Replace(string(generated), descriptionsStart, descriptionsStart+"\n"+descriptionsStart, 1),
		"duplicate end":    strings.Replace(string(generated), descriptionsEnd, descriptionsEnd+"\n"+descriptionsEnd, 1),
		"reversed":         strings.Replace(strings.Replace(string(generated), descriptionsStart, "PLACEHOLDER", 1), descriptionsEnd, descriptionsStart, 1),
		"unmarked heading": string(descriptionPage()) + "\n" + descriptionsHeading + "\n",
		"closed heading":   string(descriptionPage()) + "\n## Capability descriptions ##\n",
		"tabbed heading":   string(descriptionPage()) + "\n##\tCapability descriptions\n",
		"setext heading":   string(descriptionPage()) + "\nCapability descriptions\n---\n",
	}
	cases["reversed"] = strings.Replace(cases["reversed"], "PLACEHOLDER", descriptionsEnd, 1)
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "capabilities.md")
			require.NoError(t, os.WriteFile(path, []byte(body), 0644))
			require.Error(t, WritePage(path, c))
			actual, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, body, string(actual))
		})
	}
}
