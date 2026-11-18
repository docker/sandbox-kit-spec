package capabilitycatalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const rev = "cc033f80d58bb9516db1c26b71a3ce8184100fd0"
const tag = "v3.0.0-m.14"

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		name := filepath.Join(dir, filepath.FromSlash(path))
		require.NoError(t, os.MkdirAll(filepath.Dir(name), 0755))
		require.NoError(t, os.WriteFile(name, []byte(body), 0644))
	}
	write("schema/kit.schema.json", `{"properties":{"schemaVersion":{"const":"3"}},"definitions":{"capability":{"allOf":[{"if":{"properties":{"type":{"const":"com.docker.sandbox/zeta@1"}}},"then":{"properties":{"config":{"$ref":"capabilities/com.docker.sandbox/zeta@1.schema.json"}}}},{"if":{"properties":{"type":{"const":"com.docker.sandbox/alpha@2"}}},"then":{"properties":{"config":{"$ref":"capabilities/com.docker.sandbox/alpha@2.schema.json"}}}}]}}}`)
	for _, id := range []string{"alpha@2", "zeta@1"} {
		write("schema/capabilities/com.docker.sandbox/"+id+".schema.json", `{"title":"com.docker.sandbox/`+id+`"}`)
		write("docs/spec/capabilities/com.docker.sandbox/"+id+".md", "# "+id+"\n")
	}
	return dir
}

func TestBuildValidatesAndSorts(t *testing.T) {
	dir := fixture(t)
	c, err := Build(dir, tag, rev)
	require.NoError(t, err)
	require.Len(t, c.Capabilities, 2)
	require.Equal(t, "com.docker.sandbox/alpha@2", c.Capabilities[0].ID)
	require.Equal(t, "com.docker.sandbox/zeta@1", c.Capabilities[1].ID)
	data, err := c.JSON()
	require.NoError(t, err)
	again, err := Build(dir, tag, rev)
	require.NoError(t, err)
	data2, err := again.JSON()
	require.NoError(t, err)
	require.Equal(t, data, data2)
	for _, tc := range []struct{ name, path, from, to string }{
		{"duplicate", "schema/kit.schema.json", "com.docker.sandbox/zeta@1", "com.docker.sandbox/alpha@2"},
		{"bad title", "schema/capabilities/com.docker.sandbox/alpha@2.schema.json", "com.docker.sandbox/alpha@2", "wrong"},
		{"bad ref", "schema/kit.schema.json", "capabilities/com.docker.sandbox/zeta@1.schema.json", "capabilities/com.docker.sandbox/alpha@2.schema.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original, err := os.ReadFile(filepath.Join(dir, tc.path))
			require.NoError(t, err)
			mutated := strings.Replace(string(original), tc.from, tc.to, 1)
			require.NoError(t, os.WriteFile(filepath.Join(dir, tc.path), []byte(mutated), 0644))
			_, err = Build(dir, tag, rev)
			require.Error(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, tc.path), original, 0644))
		})
	}
	require.NoError(t, os.Remove(filepath.Join(dir, "docs/spec/capabilities/com.docker.sandbox/zeta@1.md")))
	_, err = Build(dir, tag, rev)
	require.Error(t, err)
}

func TestNotesClassifyContentChanges(t *testing.T) {
	dir := fixture(t)
	current, err := Build(dir, tag, rev)
	require.NoError(t, err)
	old := current
	old.Capabilities = append([]Entry(nil), current.Capabilities...)
	old.Version = "v3.0.0-m.13"
	old.Capabilities[0].SchemaURL = strings.Replace(old.Capabilities[0].SchemaURL, tag, old.Version, 1)
	old.Capabilities[0].SpecURL = strings.Replace(old.Capabilities[0].SpecURL, tag, old.Version, 1)
	old.Capabilities[0].SpecSHA256 = strings.Repeat("a", 64)
	old.Capabilities = old.Capabilities[:1]
	notes, err := Notes(current, &old)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(notes, "\n"))
	require.False(t, strings.HasSuffix(notes, "\n\n"))
	require.Contains(t, notes, "### Added\n\n- `com.docker.sandbox/zeta@1`")
	require.Contains(t, notes, "### Changed specification or schema\n\n- `com.docker.sandbox/alpha@2`")
	first, err := Notes(current, nil)
	require.NoError(t, err)
	require.NotContains(t, first, "### Changed specification or schema\n\n- ")
}

func TestPagePreservesSupportAndFailsClosed(t *testing.T) {
	dir := fixture(t)
	c, err := Build(dir, tag, rev)
	require.NoError(t, err)
	page := []byte("## Support by capability\n\nAll capability names in the table have the `com.docker.sandbox/` prefix.\n\n" + tableHeader + "\n" + tableRule + "\n| [`alpha@2`](https://example.test/old) | v0.45.0 | Limited: v0.51.0 | Keep `detail` exactly. |\n\n## Other section\nKeep me.\n")
	next, err := UpdatePage(page, c)
	require.NoError(t, err)
	require.Contains(t, string(next), "| v0.45.0 | Limited: v0.51.0 | Keep `detail` exactly. |")
	require.Contains(t, string(next), "| Not verified | Not verified | Docker Sandboxes support has not been verified. |")
	require.True(t, strings.HasSuffix(string(next), "## Other section\nKeep me.\n"))
	next2, err := UpdatePage(next, c)
	require.NoError(t, err)
	require.Equal(t, next, next2)
	for _, bad := range []string{strings.Replace(string(page), "alpha@2", "unknown@1", 1), strings.Replace(string(page), "\n\n## Other", "\n| [`alpha@2`](x) | x | x | x |\n\n## Other", 1), strings.Replace(string(page), tableHeader, "| Wrong |", 1)} {
		_, err := UpdatePage([]byte(bad), c)
		require.Error(t, err, "accepted bad page: %q", bad)
	}
	out := filepath.Join(t.TempDir(), "page.md")
	require.NoError(t, os.WriteFile(out, page, 0644))
	malformed := c
	malformed.Capabilities = []Entry{{ID: "wrong"}}
	require.Error(t, WritePage(out, malformed))
	actual, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, page, actual)
}

func TestJSONShape(t *testing.T) {
	c, err := Build(fixture(t), tag, rev)
	require.NoError(t, err)
	b, err := c.JSON()
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(b, &parsed))
	require.Equal(t, float64(1), parsed["format_version"])
	require.Equal(t, "3", parsed["schema_version"])
}

func TestReadCatalogRejectsTamperedMetadataBeforePageWrite(t *testing.T) {
	c, err := Build(fixture(t), tag, rev)
	require.NoError(t, err)
	page := []byte("All capability names in the table have the `com.docker.sandbox/` prefix.\n\n" + tableHeader + "\n" + tableRule + "\n| [`alpha@2`](https://example.test/old) | v0.45.0 | Unsupported | Keep this claim. |\n")
	pagePath := filepath.Join(t.TempDir(), "capabilities.md")
	require.NoError(t, os.WriteFile(pagePath, page, 0644))
	cases := []struct {
		name   string
		mutate func(*Catalog)
	}{
		{"schema path", func(c *Catalog) { c.Capabilities[0].SchemaURL += "/other" }},
		{"spec path", func(c *Catalog) { c.Capabilities[0].SpecURL += "/other" }},
		{"spec close paren", func(c *Catalog) { c.Capabilities[0].SpecURL += ")" }},
		{"spec newline", func(c *Catalog) { c.Capabilities[0].SpecURL += "\nmalicious" }},
		{"invalid version", func(c *Catalog) { c.Version = "../main" }},
		{"invalid revision", func(c *Catalog) { c.Revision = "short" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := c
			bad.Capabilities = append([]Entry(nil), c.Capabilities...)
			tc.mutate(&bad)
			encoded, err := json.Marshal(bad)
			require.NoError(t, err)
			catalogPath := filepath.Join(t.TempDir(), "catalog.json")
			require.NoError(t, os.WriteFile(catalogPath, encoded, 0644))
			_, err = ReadCatalog(catalogPath)
			require.Error(t, err)
			require.Error(t, WritePage(pagePath, bad))
			actual, err := os.ReadFile(pagePath)
			require.NoError(t, err)
			require.Equal(t, page, actual)
		})
	}
}

func TestPagePreservesTwentyOneExistingSupportRows(t *testing.T) {
	root := filepath.Clean("../..")
	c, err := Build(root, tag, rev)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(c.Capabilities), 21)
	var page strings.Builder
	page.WriteString("All capability names in the table have the `com.docker.sandbox/` prefix.\n\n" + tableHeader + "\n" + tableRule + "\n")
	for _, e := range c.Capabilities[:21] {
		page.WriteString("| [`" + strings.TrimPrefix(e.ID, "com.docker.sandbox/") + "`](https://example.test/old) | v0.45.0 | Limited: v0.51.0 | Restriction for " + e.ID + ". |\n")
	}
	page.WriteString("\n## Following section\nIntact.\n")
	next, err := UpdatePage([]byte(page.String()), c)
	require.NoError(t, err)
	require.Equal(t, 21, strings.Count(string(next), "| v0.45.0 | Limited: v0.51.0 | Restriction for "))
	require.Equal(t, len(c.Capabilities)-21, strings.Count(string(next), "| Not verified | Not verified |"))
	require.True(t, strings.HasSuffix(string(next), "## Following section\nIntact.\n"))
}
