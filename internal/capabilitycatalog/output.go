package capabilitycatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Previous builds a catalog from a previous release's committed schema and pages.
// A resolved commit SHA is used for git object paths, so a supplied ref cannot
// inject git show options or an arbitrary path.
func Previous(root, ref string) (Catalog, error) {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	out, err := cmd.Output()
	if err != nil {
		return Catalog{}, fmt.Errorf("resolve previous ref %q: %w", ref, err)
	}
	sha := strings.TrimSpace(string(out))
	if len(sha) != 40 {
		return Catalog{}, fmt.Errorf("previous ref %q has non-SHA revision", ref)
	}
	read := func(path string) ([]byte, error) {
		return exec.Command("git", "-C", root, "show", sha+":"+path).Output()
	}
	return build(read, ref, sha)
}

func Notes(current Catalog, previous *Catalog) (string, error) {
	if err := current.Validate(); err != nil {
		return "", err
	}
	older := map[string]Entry{}
	if previous != nil {
		if err := previous.Validate(); err != nil {
			return "", fmt.Errorf("previous catalog: %w", err)
		}
		for _, e := range previous.Capabilities {
			older[e.ID] = e
		}
	}
	added, changed, removed := []Entry{}, []Entry{}, []Entry{}
	for _, e := range current.Capabilities {
		old, ok := older[e.ID]
		if !ok {
			added = append(added, e)
		} else if old.SchemaContractSHA256 != e.SchemaContractSHA256 || old.SpecSHA256 != e.SpecSHA256 {
			changed = append(changed, e)
		}
		delete(older, e.ID)
	}
	for _, e := range older {
		removed = append(removed, e)
	}
	sort.Slice(removed, func(i, j int) bool { return removed[i].ID < removed[j].ID })
	var b strings.Builder
	fmt.Fprintf(&b, "## Capability catalog for %s\n\n", current.Version)
	fmt.Fprintf(&b, "The [machine-readable capability catalog](%s/releases/download/%s/capabilities.json) lists specification contracts. Runtime support depends on the implementation.\n\n", repository, current.Version)
	fmt.Fprintf(&b, "Schema version: `%s`. Full revision: `%s`.\n\n", current.SchemaVersion, current.Revision)
	writeSharedNotes(&b, current, previous)
	section := func(name string, entries []Entry) {
		fmt.Fprintf(&b, "### %s\n\n", name)
		if len(entries) == 0 {
			b.WriteString("None.\n\n")
			return
		}
		for _, e := range entries {
			fmt.Fprintf(&b, "- `%s`: [specification](%s), [schema](%s)\n", e.ID, e.SpecURL, e.SchemaURL)
		}
		b.WriteByte('\n')
	}
	section("Added", added)
	fmt.Fprint(&b, "### Changed specification or schema\n\n")
	if len(changed) == 0 {
		b.WriteString("None.\n\n")
	} else {
		for _, e := range changed {
			old := previousEntry(previous, e.ID)
			parts := []string{}
			if old.SchemaContractSHA256 != e.SchemaContractSHA256 {
				parts = append(parts, "schema")
			}
			if old.SpecSHA256 != e.SpecSHA256 {
				parts = append(parts, "specification page")
			}
			fmt.Fprintf(&b, "- `%s` (%s): [specification](%s), [schema](%s)\n", e.ID, strings.Join(parts, " and "), e.SpecURL, e.SchemaURL)
		}
		b.WriteByte('\n')
	}
	section("Removed", removed)
	section("Full inventory", current.Capabilities)
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

func writeSharedNotes(b *strings.Builder, current Catalog, previous *Catalog) {
	b.WriteString("### Shared normative specification changes\n\n")
	oldSpec, oldConformance := "", ""
	if previous != nil {
		oldSpec, oldConformance = previous.SharedSpecSHA256, previous.ConformanceSHA256
	}
	changed := false
	for _, doc := range []struct{ path, current, previous string }{
		{"SPEC-v3.md", current.SharedSpecSHA256, oldSpec},
		{"conformance.md", current.ConformanceSHA256, oldConformance},
	} {
		if doc.current != doc.previous {
			fmt.Fprintf(b, "- [%s](%s/blob/%s/docs/spec/%s)\n", doc.path, repository, current.Version, doc.path)
			changed = true
		}
	}
	if !changed {
		b.WriteString("None.\n")
	}
	b.WriteByte('\n')
}

const tableHeader = "| Capability | Local sandboxes | Cloud sandboxes | Restrictions |"
const tableRule = "| --- | --- | --- | --- |"
const note = "Not verified means Docker Sandboxes support has not been confirmed for that\ncapability. Specification links are pinned to the catalog release."

// UpdatePage updates only the support table and its brief explanation.
// Existing implementation claims are retained literally; new types are unverified.
// A row absent from the catalog fails closed so removals receive manual review
// before anyone deletes a runtime support claim from the downstream page.
func UpdatePage(page []byte, catalog Catalog) ([]byte, error) {
	if err := catalog.Validate(); err != nil {
		return nil, err
	}
	source := string(page)
	if strings.Count(source, tableHeader) != 1 {
		return nil, fmt.Errorf("expected exactly one capability support table header")
	}
	start := strings.Index(source, tableHeader)
	if start > 0 && source[start-1] != '\n' {
		return nil, fmt.Errorf("capability table header is not on its own line")
	}
	endLine := strings.IndexByte(source[start:], '\n')
	if endLine < 0 {
		return nil, fmt.Errorf("capability table has no rows")
	}
	rowsStart := start + endLine + 1
	if !strings.HasPrefix(source[rowsStart:], tableRule+"\n") {
		return nil, fmt.Errorf("capability table divider is missing")
	}
	rowsStart += len(tableRule) + 1
	rowsEnd := rowsStart
	for rowsEnd < len(source) && source[rowsEnd] == '|' {
		next := strings.IndexByte(source[rowsEnd:], '\n')
		if next < 0 {
			return nil, fmt.Errorf("unterminated capability table row")
		}
		rowsEnd += next + 1
	}
	if rowsEnd == rowsStart {
		return nil, fmt.Errorf("capability support table is empty")
	}
	known := map[string]Entry{}
	for _, e := range catalog.Capabilities {
		known[e.ID] = e
	}
	existing := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(source[rowsStart:rowsEnd], "\n"), "\n") {
		cells := strings.Split(line, " | ")
		if len(cells) != 4 || !strings.HasPrefix(cells[0], "| [`") || !strings.HasSuffix(cells[3], " |") {
			return nil, fmt.Errorf("malformed capability row %q", line)
		}
		label := strings.TrimPrefix(cells[0], "| [`")
		cut := strings.Index(label, "`](")
		if cut < 0 {
			return nil, fmt.Errorf("malformed capability link %q", line)
		}
		id := "com.docker.sandbox/" + label[:cut]
		if _, ok := known[id]; !ok {
			return nil, fmt.Errorf("stale or unknown capability %q", id)
		}
		if _, ok := existing[id]; ok {
			return nil, fmt.Errorf("duplicate capability row %q", id)
		}
		if !strings.HasSuffix(label[cut+3:], ")") {
			return nil, fmt.Errorf("malformed capability link for %q", id)
		}
		existing[id] = strings.Join(cells[1:], " | ")
	}
	var table strings.Builder
	table.WriteString(tableHeader + "\n" + tableRule + "\n")
	for _, e := range catalog.Capabilities {
		rest, ok := existing[e.ID]
		if !ok {
			rest = "Not verified | Not verified | Docker Sandboxes support has not been verified. |"
		}
		fmt.Fprintf(&table, "| [`%s`](%s) | %s\n", strings.TrimPrefix(e.ID, "com.docker.sandbox/"), e.SpecURL, rest)
	}
	result := source[:start] + table.String() + source[rowsEnd:]
	// Keep the note beside the table, without changing other prose.
	if strings.Contains(result, note) {
		return []byte(result), nil
	}
	marker := "All capability names in the table have the `com.docker.sandbox/` prefix.\n"
	if strings.Count(result, marker) != 1 {
		return nil, fmt.Errorf("capability table explanation is missing or ambiguous")
	}
	result = strings.Replace(result, marker, marker+note+"\n", 1)
	return []byte(result), nil
}

func WriteRelease(dir string, c Catalog, previous *Catalog) error {
	data, err := c.JSON()
	if err != nil {
		return err
	}
	notes, err := Notes(c, previous)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// Both outputs are prepared before either public name is replaced.
	files := []struct {
		name string
		data []byte
	}{{"capabilities.json", data}, {"capability-notes.md", []byte(notes)}}
	staged := []string{}
	defer func() {
		for _, p := range staged {
			_ = os.Remove(p)
		}
	}()
	for _, f := range files {
		temp, err := os.CreateTemp(dir, ".capability-*")
		if err != nil {
			return err
		}
		staged = append(staged, temp.Name())
		if _, err := temp.Write(f.data); err != nil {
			_ = temp.Close()
			return err
		}
		if err := temp.Close(); err != nil {
			return err
		}
	}
	for i, f := range files {
		if err := os.Rename(staged[i], filepath.Join(dir, f.name)); err != nil {
			return err
		}
	}
	return nil
}

func ReadCatalog(path string) (Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Catalog{}, err
	}
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return Catalog{}, err
	}
	return c, c.Validate()
}
func WritePage(path string, c Catalog) error {
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	next, err := UpdatePage(old, c)
	if err != nil {
		return err
	}
	if bytes.Equal(old, next) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, next, info.Mode().Perm())
}

func previousEntry(c *Catalog, id string) Entry {
	if c != nil {
		for _, e := range c.Capabilities {
			if e.ID == id {
				return e
			}
		}
	}
	return Entry{}
}
