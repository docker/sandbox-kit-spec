// Package capabilitycatalog builds a release inventory from the specification schema.
package capabilitycatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const repository = "https://github.com/docker/sandbox-kit-spec"

var (
	idPattern       = regexp.MustCompile(`^com\.docker\.sandbox/[a-z0-9][a-z0-9-]*@[1-9][0-9]*$`)
	versionPattern  = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
	revisionPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
)

// Entry records a specification contract, not a runtime implementation claim.
type Entry struct {
	ID                   string `json:"id"`
	Description          string `json:"description"`
	SchemaURL            string `json:"schema_url"`
	SpecURL              string `json:"spec_url"`
	SchemaSHA256         string `json:"schema_sha256"`
	SchemaContractSHA256 string `json:"schema_contract_sha256"`
	SpecSHA256           string `json:"spec_sha256"`
}

type Catalog struct {
	FormatVersion     int     `json:"format_version"`
	Version           string  `json:"version"`
	Revision          string  `json:"revision"`
	SchemaVersion     string  `json:"schema_version"`
	SharedSpecSHA256  string  `json:"shared_spec_sha256"`
	ConformanceSHA256 string  `json:"conformance_sha256"`
	Capabilities      []Entry `json:"capabilities"`
}

type readFile func(string) ([]byte, error)

func disk(root string) readFile {
	return func(path string) ([]byte, error) { return os.ReadFile(filepath.Join(root, filepath.FromSlash(path))) }
}

// Build validates the root schema's conditional mapping and every referenced page.
func Build(root, version, revision string) (Catalog, error) {
	return build(disk(root), version, revision)
}

func build(read readFile, version, revision string) (Catalog, error) {
	if !versionPattern.MatchString(version) {
		return Catalog{}, fmt.Errorf("invalid release tag %q", version)
	}
	if !revisionPattern.MatchString(revision) {
		return Catalog{}, fmt.Errorf("revision must be a full 40-character commit SHA")
	}
	data, err := read("schema/kit.schema.json")
	if err != nil {
		return Catalog{}, err
	}
	var root struct {
		Properties map[string]struct {
			Const string `json:"const"`
		} `json:"properties"`
		Definitions map[string]struct {
			AllOf []struct {
				If struct {
					Properties map[string]struct {
						Const string `json:"const"`
					} `json:"properties"`
				} `json:"if"`
				Then struct {
					Properties map[string]struct {
						Ref string `json:"$ref"`
					} `json:"properties"`
				} `json:"then"`
			} `json:"allOf"`
		} `json:"definitions"`
	}
	if err = json.Unmarshal(data, &root); err != nil {
		return Catalog{}, fmt.Errorf("decode root schema: %w", err)
	}
	schemaVersion := root.Properties["schemaVersion"].Const
	if schemaVersion == "" {
		return Catalog{}, errors.New("root schema has no schemaVersion const")
	}
	conditions := root.Definitions["capability"].AllOf
	if len(conditions) == 0 {
		return Catalog{}, errors.New("root schema has no capability conditional mappings")
	}
	seen := make(map[string]bool, len(conditions))
	entries := make([]Entry, 0, len(conditions))
	for conditionIndex, condition := range conditions {
		id := condition.If.Properties["type"].Const
		if !idPattern.MatchString(id) {
			return Catalog{}, fmt.Errorf("invalid or missing conditional capability ID %q", id)
		}
		if seen[id] {
			return Catalog{}, fmt.Errorf("duplicate capability ID %q", id)
		}
		seen[id] = true
		ref := condition.Then.Properties["config"].Ref
		want := "capabilities/" + id + ".schema.json"
		if ref != want {
			return Catalog{}, fmt.Errorf("capability %s: schema ref %q, want %q", id, ref, want)
		}
		schemaPath := "schema/" + ref
		specPath := "docs/spec/capabilities/" + id + ".md"
		schema, err := read(schemaPath)
		if err != nil {
			return Catalog{}, fmt.Errorf("capability %s schema: %w", id, err)
		}
		var title struct {
			Title string `json:"title"`
		}
		if err = json.Unmarshal(schema, &title); err != nil {
			return Catalog{}, fmt.Errorf("capability %s schema JSON: %w", id, err)
		}
		if title.Title != id {
			return Catalog{}, fmt.Errorf("capability %s schema title %q", id, title.Title)
		}
		doc, err := read(specPath)
		if err != nil {
			return Catalog{}, fmt.Errorf("capability %s spec page: %w", id, err)
		}
		if len(strings.TrimSpace(string(doc))) == 0 {
			return Catalog{}, fmt.Errorf("capability %s spec page is empty", id)
		}
		specURL := repository + "/blob/" + version + "/" + specPath
		description, err := descriptionFromPage(doc, id, specURL)
		if err != nil {
			return Catalog{}, err
		}
		contractHash, err := schemaContractHash(read, data, conditionIndex)
		if err != nil {
			return Catalog{}, fmt.Errorf("capability %s schema contract: %w", id, err)
		}
		entries = append(entries, Entry{ID: id, Description: description, SchemaURL: repository + "/blob/" + version + "/" + schemaPath, SpecURL: specURL, SchemaSHA256: hash(schema), SchemaContractSHA256: contractHash, SpecSHA256: hash(doc)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	shared, err := read("docs/spec/SPEC-v3.md")
	if err != nil {
		return Catalog{}, fmt.Errorf("shared specification: %w", err)
	}
	conformance, err := read("docs/spec/conformance.md")
	if err != nil {
		return Catalog{}, fmt.Errorf("conformance specification: %w", err)
	}
	if len(bytes.TrimSpace(shared)) == 0 || len(bytes.TrimSpace(conformance)) == 0 {
		return Catalog{}, errors.New("empty shared specification document")
	}
	return Catalog{FormatVersion: 1, Version: version, Revision: revision, SchemaVersion: schemaVersion, SharedSpecSHA256: hash(shared), ConformanceSHA256: hash(conformance), Capabilities: entries}, nil
}

func hash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

// Validate rejects malformed snapshots before they can change the downstream page.
func (c Catalog) Validate() error {
	if c.FormatVersion != 1 || !versionPattern.MatchString(c.Version) || !revisionPattern.MatchString(c.Revision) || c.SchemaVersion == "" || len(c.Capabilities) == 0 {
		return errors.New("invalid or empty capability catalog")
	}
	for _, digest := range []string{c.SharedSpecSHA256, c.ConformanceSHA256} {
		if decoded, err := hex.DecodeString(digest); err != nil || len(decoded) != sha256.Size {
			return errors.New("invalid shared specification hash")
		}
	}
	last := ""
	for _, e := range c.Capabilities {
		if !idPattern.MatchString(e.ID) || e.ID <= last {
			return fmt.Errorf("invalid, duplicate, or unsorted capability ID %q", e.ID)
		}
		wantSchema := repository + "/blob/" + c.Version + "/schema/capabilities/" + e.ID + ".schema.json"
		wantSpec := repository + "/blob/" + c.Version + "/docs/spec/capabilities/" + e.ID + ".md"
		if e.SchemaURL != wantSchema || e.SpecURL != wantSpec {
			return fmt.Errorf("noncanonical URLs for %s", e.ID)
		}
		if err := validateDescription(e.Description, e.SpecURL); err != nil {
			return fmt.Errorf("invalid description for %s: %w", e.ID, err)
		}
		for _, digest := range []string{e.SchemaSHA256, e.SchemaContractSHA256, e.SpecSHA256} {
			if len(digest) != 64 {
				return fmt.Errorf("invalid hash for %s", e.ID)
			}
			if _, err := hex.DecodeString(digest); err != nil {
				return fmt.Errorf("invalid hash for %s: %w", e.ID, err)
			}
		}
		last = e.ID
	}
	return nil
}

func (c Catalog) JSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	return append(b, '\n'), err
}
