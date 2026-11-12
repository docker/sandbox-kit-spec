package kit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"

	"gopkg.in/yaml.v3"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// Inspection is what a kit says about itself: the descriptor it was
// published with, and the sources it staged into its own filesystem.
type Inspection struct {
	// Descriptor is the published descriptor exactly as the manifest
	// annotation carries it, and the document of record: compact JSON, or
	// YAML on kits published before the annotation became JSON.
	Descriptor []byte

	// Stem names the kit's own root under StagedKitRoot, empty when no
	// staged root holds this kit's descriptor.
	Stem string

	// Recipe is the staged content recipe, whichever form it was authored
	// in — companion, dockerfile:, inline build:, or comment descriptor.
	// Nil for a declaration-only kit and for a set, which have none.
	Recipe []byte
}

// RecipePath is where the recipe is staged in the kit's filesystem.
func (i *Inspection) RecipePath() string {
	if i.Stem == "" {
		return ""
	}
	return path.Join(StagedKitRoot, i.Stem, stagedRecipeName)
}

// DescriptorPath is where the descriptor is staged in the kit's filesystem.
func (i *Inspection) DescriptorPath() string {
	if i.Stem == "" {
		return ""
	}
	return path.Join(StagedKitRoot, i.Stem, stagedDescriptorName)
}

// Inspect reads a kit's descriptor and recipe without judging either. The
// recipe is looked up under the same staged root the conformance checks
// pick, so in an artifact carrying several kits' sources it is this kit's
// recipe and not one it was built from.
//
// Nothing is judged, so a descriptor the strict decoder refuses — an
// unknown field, another schemaVersion — is still shown; it only has to
// parse as YAML.
func Inspect(ctx context.Context, a Artifact) (*Inspection, error) {
	in, doc, err := inspectDescriptor(a)
	if err != nil {
		return nil, err
	}
	stems, err := a.StagedStems(ctx)
	if err != nil {
		return nil, fmt.Errorf("list staged kit roots: %w", err)
	}
	if d, err := spec.Decode(in.Descriptor); err == nil {
		in.Stem = ownStem(ctx, a, d, stems)
	} else {
		in.Stem = ownStemAsDocument(ctx, a, doc, stems)
	}
	if in.Stem == "" {
		return in, nil
	}
	recipe, ok, err := a.ReadFile(ctx, in.RecipePath())
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", in.RecipePath(), err)
	}
	if ok {
		in.Recipe = recipe
	}
	return in, nil
}

// InspectDescriptor reads only the descriptor, from the manifest
// annotation. It touches no layer, so it costs no more than the manifest
// fetch; the Inspection it returns has no Stem and no Recipe.
func InspectDescriptor(a Artifact) (*Inspection, error) {
	in, _, err := inspectDescriptor(a)
	return in, err
}

func inspectDescriptor(a Artifact) (*Inspection, any, error) {
	raw := a.Annotations()[spec.AnnotationDescriptor]
	if raw == "" {
		return nil, nil, fmt.Errorf("manifest carries no %s annotation: %w", spec.AnnotationDescriptor, ErrNotAKit)
	}
	var doc any
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, nil, fmt.Errorf("descriptor annotation does not parse: %w", err)
	}
	return &Inspection{Descriptor: []byte(raw)}, doc, nil
}

// ownStemAsDocument is ownStem for a descriptor the grammar refuses: the
// staged root whose kit.yaml is the same document as the annotation,
// compared as parsed YAML since neither decodes into a Descriptor.
func ownStemAsDocument(ctx context.Context, a Artifact, doc any, stems []string) string {
	if stem, present, err := sourceStemLabel(ctx, a); err != nil || present {
		return stem
	}
	if len(stems) == 1 {
		return stems[0]
	}
	want, err := json.Marshal(doc)
	if err != nil {
		return ""
	}
	for _, stem := range stems {
		raw, ok, err := a.ReadFile(ctx, path.Join(StagedKitRoot, stem, stagedDescriptorName))
		if err != nil || !ok {
			continue
		}
		var staged any
		if yaml.Unmarshal(raw, &staged) != nil {
			continue
		}
		if got, err := json.Marshal(staged); err == nil && bytes.Equal(got, want) {
			return stem
		}
	}
	return ""
}
