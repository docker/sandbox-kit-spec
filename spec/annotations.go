package spec

import (
	"encoding/json"
	"strings"
)

// BuiltBy is the value of AnnotationBuiltBy: which frontend build
// published a kit. JSON rather than one packed string because the fields
// are read apart — a version is compared, a revision is looked up — and
// because an object can gain a field without every reader learning a new
// way to split one.
type BuiltBy struct {
	// Name is the frontend's published image, e.g. docker/sandbox-kit.
	Name string `json:"name"`
	// Version is the frontend's release, or "dev" for a build no release
	// stamped.
	Version string `json:"version"`
	// Revision is the full commit the frontend was built from, omitted
	// when the build recorded none.
	Revision string `json:"revision,omitempty"`
}

// Marshal renders the annotation's value: compact JSON, matching the
// published descriptor's own encoding, and byte-deterministic for a given
// build — a kit rebuilt by the same frontend from the same descriptor
// keeps its digest.
func (b BuiltBy) Marshal() (string, error) {
	out, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ParseBuiltBy reads the annotation's value. The false report is for an
// absent annotation, which is the ordinary case for a kit published
// before the annotation existed, not a malformed one.
func ParseBuiltBy(annotations map[string]string) (BuiltBy, bool, error) {
	raw, ok := annotations[AnnotationBuiltBy]
	if !ok {
		return BuiltBy{}, false, nil
	}
	var b BuiltBy
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return BuiltBy{}, true, err
	}
	return b, true, nil
}

// Standard OCI image annotation keys the frontend populates from the
// descriptor, so generic registry tooling displays a kit's metadata
// without knowing the kit grammar. All of them are display metadata with
// exactly the descriptor fields' authority: self-asserted, never trust
// inputs.
const (
	OCIAnnotationTitle       = "org.opencontainers.image.title"
	OCIAnnotationDescription = "org.opencontainers.image.description"
	OCIAnnotationAuthors     = "org.opencontainers.image.authors"
	OCIAnnotationSource      = "org.opencontainers.image.source"
	OCIAnnotationLicenses    = "org.opencontainers.image.licenses"
	OCIAnnotationVersion     = "org.opencontainers.image.version"
)

// OCIAnnotations maps a published descriptor onto the standard
// org.opencontainers.image.* annotations it can populate: title,
// description, authors, source, and licenses straight from their fields,
// and version from the version: fallback or — when every versioned
// provides entry agrees — that one version. Empty fields yield no key,
// so absence means "not declared". Builder inputs (created, revision,
// base.*) are never emitted here: the descriptor cannot answer them.
// A builder may separately emit created from SOURCE_DATE_EPOCH per §9.3;
// this projection has no timestamp input and never reads the wall clock.
func OCIAnnotations(d *Descriptor) map[string]string {
	out := map[string]string{}
	set := func(key, value string) {
		if value != "" {
			out[key] = value
		}
	}
	set(OCIAnnotationTitle, d.DisplayName)
	set(OCIAnnotationDescription, d.Description)
	set(OCIAnnotationAuthors, d.Author)
	set(OCIAnnotationSource, d.SourceURL)
	set(OCIAnnotationLicenses, strings.Join(d.Licenses, ","))
	set(OCIAnnotationVersion, descriptorVersion(d))
	return out
}

// descriptorVersion is the one version the descriptor names: the
// version: field when set, otherwise the version every versioned
// provides entry agrees on. Disagreement or absence yields "" — an
// ambiguous version is worse than none.
//
// Derived entries do not vote. A kit's packages carry hundreds of
// unrelated versions, so counting them would leave every kit that named
// no version: of its own with no version annotation at all — the
// agreement rule would find the disagreement of a Debian archive rather
// than the kit's own silence.
func descriptorVersion(d *Descriptor) string {
	if d.Version != "" {
		return d.Version
	}
	version := ""
	for _, s := range d.Provides {
		p, err := ParseProvide(s)
		if err != nil || p.Version == "" || IsDerivedProvide(p.Name) {
			continue
		}
		if version != "" && version != p.Version {
			return ""
		}
		version = p.Version
	}
	return version
}
