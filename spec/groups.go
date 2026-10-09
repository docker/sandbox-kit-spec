package spec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// CapabilityItem is the descriptor union: an ordinary Capability or Group.
// The alias keeps existing ordinary-entry Go literals source-compatible.
type CapabilityItem = Capability

// CapabilityGroup selects one feature's requests atomically.
type CapabilityGroup struct {
	Name         string       `json:"name,omitempty" yaml:"name,omitempty"`
	Description  string       `json:"description,omitempty" yaml:"description,omitempty"`
	Optional     bool         `json:"optional,omitempty" yaml:"optional,omitempty"`
	Capabilities []Capability `json:"capabilities" yaml:"capabilities"`
}

func (g *CapabilityGroup) UnmarshalYAML(unmarshal func(any) error) error {
	type plain CapabilityGroup
	if err := unmarshal((*plain)(g)); err != nil {
		return err
	}
	var keys map[string]yaml.Node
	if err := unmarshal(&keys); err != nil {
		return err
	}
	for _, field := range []string{"name", "description"} {
		if n, ok := keys[field]; ok && n.ShortTag() != "!!str" {
			return fmt.Errorf("group %s must be a string", field)
		}
	}
	if n, ok := keys["optional"]; ok && n.ShortTag() != "!!bool" {
		return fmt.Errorf("group optional must be a boolean")
	}
	return nil
}

func (g *CapabilityGroup) UnmarshalJSON(data []byte) error {
	type plain CapabilityGroup
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode((*plain)(g)); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	for _, field := range []string{"name", "description"} {
		if n, ok := keys[field]; ok && bytes.Equal(bytes.TrimSpace(n), []byte("null")) {
			return fmt.Errorf("group %s must be a string", field)
		}
	}
	if n, ok := keys["optional"]; ok && bytes.Equal(bytes.TrimSpace(n), []byte("null")) {
		return fmt.Errorf("group optional must be a boolean")
	}
	return nil
}

// CapabilitySource survives publication for diagnostics, not trust decisions.
type CapabilitySource struct {
	Kit  string `json:"kit" yaml:"kit"`
	Path string `json:"path" yaml:"path"`
}

func (s *CapabilitySource) UnmarshalYAML(unmarshal func(any) error) error {
	type plain CapabilitySource
	if err := unmarshal((*plain)(s)); err != nil {
		return err
	}
	var keys map[string]yaml.Node
	if err := unmarshal(&keys); err != nil {
		return err
	}
	for _, field := range []string{"kit", "path"} {
		if n, ok := keys[field]; ok && n.ShortTag() != "!!str" {
			return fmt.Errorf("source %s must be a string", field)
		}
	}
	return nil
}

func (s *CapabilitySource) UnmarshalJSON(data []byte) error {
	type plain CapabilitySource
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode((*plain)(s)); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	for _, field := range []string{"kit", "path"} {
		if n, ok := keys[field]; ok && bytes.Equal(bytes.TrimSpace(n), []byte("null")) {
			return fmt.Errorf("source %s must be a string", field)
		}
	}
	return nil
}

// SelectCapability answers whether the runtime will provide one expanded entry.
// The descriptor is the owning Kit after argument and environment expansion,
// including its DisplayName and all declarations before selection. Descriptor
// and capability inputs are passed by value. The context carries cancellation
// and deadlines.
// It must not apply the entry: the enclosing group can still be rejected.
type SelectCapability func(context.Context, Descriptor, Capability) CapabilityDecision

// CapabilityDecision is the runtime's answer for one expanded entry.
// Its zero value rejects the entry. Message explains acceptance or rejection
// for selection records and, on required rejection, error diagnostics.
type CapabilityDecision struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message,omitempty"`
}

// Supported constructs a selector from a runtime's claimed type list.
func Supported(types ...string) SelectCapability {
	claimed := make(map[string]bool, len(types))
	for _, typ := range types {
		claimed[typ] = true
	}
	return func(_ context.Context, _ Descriptor, c Capability) CapabilityDecision {
		if claimed[c.Type] {
			return CapabilityDecision{Accepted: true}
		}
		return CapabilityDecision{Message: "unsupported capability type " + c.Type}
	}
}

// KnownCapabilities lists types understood by this version of the library.
// Understanding a schema is not a claim that a runtime implements it.
func KnownCapabilities() []string {
	return []string{CapabilityNetworkPolicy, CapabilityNetworkPolicyV2, CapabilityCredential, CapabilitySSHAgent,
		CapabilityVolume, CapabilityHostMount, CapabilityPort, CapabilityUSBDevice, CapabilityResources,
		CapabilityPrivileged, CapabilityLifecycle, CapabilityAgentContext, CapabilityAgentSessions, CapabilityAgentInteractiveSessions,
		CapabilityAgentSkills, CapabilityAgentSkill, CapabilitySbx, CapabilityLongRunning, CapabilityGitIdentity, CapabilityKitRegistry}
}

// SelectionRecord identifies an original item and its evaluated members.
// Path is in the consumed descriptor; Source, when present, is publisher metadata.
type SelectionRecord struct {
	Path          string             `json:"path"`
	Name          string             `json:"name,omitempty"`
	Source        *CapabilitySource  `json:"source,omitempty"`
	Members       []string           `json:"members"`
	MemberSources []CapabilitySource `json:"memberSources,omitempty"`
	Rejected      []string           `json:"rejected,omitempty"`
	// Decisions follows Members order, including accepted members of skipped
	// groups. Older persisted records may omit it.
	Decisions []CapabilityDecision `json:"decisions,omitempty"`
}

// Selection retains the create-time decision separately from merged grants.
type Selection struct {
	Capabilities []Capability      `json:"capabilities"`
	Selected     []SelectionRecord `json:"selected,omitempty"`
	Skipped      []SelectionRecord `json:"skipped,omitempty"`
}

// HasGroups reports whether selection is needed before reconciliation.
func HasGroups(items []Capability) bool {
	for _, c := range items {
		if c.Group != nil || c.groupSet {
			return true
		}
	}
	return false
}

// MapCapabilities visits ordinary entries, including group members, without
// selecting them. It copies slices and groups; fn must copy a config before
// changing it. Publishers use it to stage every potentially selected body.
func MapCapabilities(items []Capability, fn func(Capability) (Capability, error)) ([]Capability, error) {
	out := make([]Capability, len(items))
	for i, c := range items {
		if c.Group == nil {
			var err error
			out[i], err = fn(c)
			if err != nil {
				return nil, err
			}
		} else {
			g := *c.Group
			var err error
			g.Capabilities, err = MapCapabilities(g.Capabilities, fn)
			if err != nil {
				return nil, err
			}
			c.Group = &g
			out[i] = c
		}
	}
	return out, nil
}

// DeclaredCapabilities enumerates ordinary requests, including unselected
// members. Use only for artifact inspection, never runtime application.
func DeclaredCapabilities(items []Capability) []Capability {
	var out []Capability
	for _, c := range items {
		if c.Group != nil {
			out = append(out, DeclaredCapabilities(c.Group.Capabilities)...)
		} else {
			out = append(out, c)
		}
	}
	return out
}

func validateCapabilityEntries(d *Descriptor) error {
	if !HasGroups(d.Capabilities) {
		return validateCapabilityBlock(d)
	}
	var errs ValidationErrors
	var ordinary []Capability
	var paths []string
	for i, c := range d.Capabilities {
		at := fmt.Sprintf("capabilities[%d]", i)
		if c.Source != nil && c.Source.Path == "" {
			errs.add(fieldErrorf(at+".source", "path is required"))
		}
		if c.Group == nil && !c.groupSet {
			ordinary = append(ordinary, c)
			paths = append(paths, at)
			continue
		}
		if c.Group == nil {
			errs.add(fieldErrorf(at+".group", "group must be an object"))
			continue
		}
		if c.Type != "" || c.Name != "" || c.Description != "" || c.Optional || c.optionalSet || c.ConfigStated() {
			errs.add(fieldErrorf(at, "item must be exactly one ordinary entry or group"))
		}
		if len(c.Group.Capabilities) == 0 {
			errs.add(fieldErrorf(at+".group.capabilities", "group must contain at least one member"))
		}
		cp := *d
		cp.declarationsOnly = true
		cp.Capabilities = c.Group.Capabilities
		memberPaths := make([]string, len(cp.Capabilities))
		for j, member := range cp.Capabilities {
			memberPaths[j] = fmt.Sprintf("%s.group.capabilities[%d]", at, j)
			if member.Group != nil || member.groupSet {
				errs.add(fieldErrorf(memberPaths[j], "nested groups are not permitted"))
			}
			if member.Optional || member.optionalSet {
				errs.add(fieldErrorf(memberPaths[j]+".optional", "group members must not state optional"))
			}
		}
		errs.add(remapCapabilityErrors(validateCapabilityBlock(&cp), memberPaths))
	}
	cp := *d
	cp.Capabilities = ordinary
	cp.declarationsOnly = true
	errs.add(remapCapabilityErrors(validateCapabilityBlock(&cp), paths))
	return errs.err()
}

func remapCapabilityErrors(err error, paths []string) error {
	if err == nil {
		return nil
	}
	if all, ok := err.(ValidationErrors); ok {
		out := make(ValidationErrors, len(all))
		for i, e := range all {
			out[i] = remapCapabilityErrors(e, paths)
		}
		return out
	}
	if field, ok := err.(*FieldError); ok {
		replacements := make([]string, 0, 2*len(paths))
		for i, to := range paths {
			replacements = append(replacements, fmt.Sprintf("capabilities[%d]", i), to)
		}
		for i, to := range paths {
			from := fmt.Sprintf("capabilities[%d]", i)
			if field.Path == from || strings.HasPrefix(field.Path, from+".") {
				return &FieldError{Path: to + strings.TrimPrefix(field.Path, from), err: &capabilityPathError{
					cause: field.err, paths: strings.NewReplacer(replacements...),
				}}
			}
		}
	}
	return err
}

// Remap every referenced index in one pass, including the other side of a
// duplicate, without losing typed causes used by errors.Is and errors.As.
type capabilityPathError struct {
	cause error
	paths *strings.Replacer
}

func (e *capabilityPathError) Error() string { return e.paths.Replace(e.cause.Error()) }
func (e *capabilityPathError) Unwrap() error { return e.cause }

// ValidateDeclarations validates all structure and per-entry configurations,
// leaving selection-dependent cross-entry checks to ValidateEffective.
func ValidateDeclarations(raw []byte, d *Descriptor) ([]string, error) {
	cp := *d
	cp.declarationsOnly = true
	return ValidateRaw(raw, &cp)
}

// ValidateExpandedDeclarations additionally refuses unresolved arguments
// and environment references.
func ValidateExpandedDeclarations(raw []byte, d *Descriptor) ([]string, error) {
	cp := *d
	cp.declarationsOnly = true
	return ValidateEffective(raw, &cp)
}

// SelectCapabilities validates declarations in their actual descriptor context
// before calling the selector and flattens only wholly selected constructs.
// A nil descriptor or selector is an error, never an implicit default.
// Required rejection returns records alongside the error for diagnostics.
// Cancellation returns a zero selection and the context error.
func SelectCapabilities(ctx context.Context, d *Descriptor, selectCapability SelectCapability) (Selection, error) {
	var result Selection
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if selectCapability == nil {
		return result, fmt.Errorf("select capabilities: no selector")
	}
	if d == nil {
		return result, fmt.Errorf("select capabilities: no descriptor")
	}
	cp := *d
	cp.declarationsOnly = true
	if _, err := Validate(&cp); err != nil {
		return result, err
	}
	items := d.Capabilities
	for i, item := range items {
		raw, err := json.Marshal(item)
		if err != nil {
			return result, fmt.Errorf("select capabilities: capabilities[%d]: %w", i, err)
		}
		if argRef.Match(raw) {
			return result, fmt.Errorf("select capabilities: capabilities[%d] still contains unresolved arguments", i)
		}
	}
	if HasEnvReferences(items) {
		return result, fmt.Errorf("select capabilities: expand the final container environment before selection")
	}
	var errs ValidationErrors
	for i, item := range items {
		at := fmt.Sprintf("capabilities[%d]", i)
		record := SelectionRecord{Path: at, Name: item.Name, Source: item.Source}
		if record.Source != nil {
			source := *record.Source
			record.Source = &source
		}
		optional := item.Optional
		members := []Capability{item}
		if item.Group != nil {
			members = item.Group.Capabilities
			optional = item.Group.Optional
			record.Name = item.Group.Name
		}
		var selected []Capability
		for j, c := range members {
			memberPath := at
			if item.Group != nil {
				memberPath = fmt.Sprintf("%s.group.capabilities[%d]", at, j)
			}
			record.Members = append(record.Members, memberPath)
			origin := CapabilitySource{Path: memberPath}
			if c.Source != nil {
				origin = *c.Source
			}
			record.MemberSources = append(record.MemberSources, origin)
			if err := ctx.Err(); err != nil {
				return Selection{}, err
			}
			decision := selectCapability(ctx, *d, c)
			if err := ctx.Err(); err != nil {
				return Selection{}, err
			}
			record.Decisions = append(record.Decisions, decision)
			if !decision.Accepted {
				record.Rejected = append(record.Rejected, memberPath)
				if !optional {
					detail := fmt.Sprintf("required capability selection rejected (item %s)", at)
					if origin.Kit != "" || origin.Path != memberPath {
						detail += fmt.Sprintf("; original source %s %s", origin.Kit, origin.Path)
					}
					if decision.Message != "" {
						detail += "; " + decision.Message
					}
					errs.add(fieldErrorf(memberPath, "%s", detail))
				}
			}
			if c.Source == nil {
				c.Source = &CapabilitySource{Path: memberPath}
			}
			selected = append(selected, c)
		}
		if len(record.Rejected) > 0 {
			result.Skipped = append(result.Skipped, record)
		} else {
			result.Selected = append(result.Selected, record)
			result.Capabilities = append(result.Capabilities, selected...)
		}
	}
	return result, errs.err()
}

func contributionsNeedDeferredMerge(contributions []Contribution) bool {
	for _, c := range contributions {
		if c.Descriptor == nil {
			continue
		}
		if HasGroups(c.Descriptor.Capabilities) || HasEnvReferences(c.Descriptor.Capabilities) {
			return true
		}
		for _, item := range c.Descriptor.Capabilities {
			// A set may re-export text inputs such as volume size. Keep
			// their requests separate until create expands the values:
			// numeric equivalence cannot judge unresolved placeholders.
			if item.Type == CapabilityVolume && capabilityIsParameterized(item) {
				return true
			}
		}
	}
	return false
}

// preserveGroups preserves selection boundaries and unresolved declarations.
// Concrete, ungrouped storage still reconciles at publication.
// Wrapping the remaining ordinary entries preserves their optionality and
// allows independent singleton contributions without a publishing-only grammar.
func preserveGroups(contributions []Contribution, opts MergeOptions) (*MergeResult, error) {
	stripped := make([]Contribution, len(contributions))
	for i, c := range contributions {
		if c.Descriptor == nil {
			return nil, fmt.Errorf("merge: %s has no descriptor", c.Reference)
		}
		d := *c.Descriptor
		d.Capabilities = nil
		stripped[i] = Contribution{Reference: c.Reference, Descriptor: &d}
	}
	result, err := mergeDeclarations(stripped)
	if err != nil {
		return nil, err
	}
	count := 0
	storage := capabilityMerge{byKey: map[string]keyed{}, optional: map[string]bool{}}
	storagePositions := map[string]int{}
	for _, contribution := range contributions {
		for i, item := range contribution.Descriptor.Capabilities {
			source := &CapabilitySource{Kit: contribution.Reference, Path: fmt.Sprintf("capabilities[%d]", i)}
			if item.Source != nil {
				*source = *item.Source
				if source.Kit == "" {
					source.Kit = contribution.Reference
				}
			}
			if item.Group == nil && (item.Type == CapabilityVolume || item.Type == CapabilityHostMount) && !capabilityIsParameterized(item) {
				// An unrelated deferred request cannot hide a concrete conflict.
				// Explicit group members stay separate until atomic selection.
				item.Source = source
				reference := fmt.Sprintf("%s (%s %s)", contribution.Reference, source.Kit, source.Path)
				if err := storage.add(reference, item); err != nil {
					return nil, err
				}
				key, err := storage.instanceKey(reference, item)
				if err != nil {
					return nil, err
				}
				merged := storage.byKey[key].capability
				merged.Optional = storage.optional[key]
				if position, seen := storagePositions[key]; seen {
					result.Descriptor.Capabilities[position] = merged
				} else {
					storagePositions[key] = len(result.Descriptor.Capabilities)
					result.Descriptor.Capabilities = append(result.Descriptor.Capabilities, merged)
				}
				// Keeping these entries ordinary lets an enclosing set also
				// reconcile them without crossing an explicit group boundary.
				continue
			}
			if item.Group == nil {
				member := item
				member.Optional = false
				member.optionalSet = false
				member.Source = source
				item = Capability{Group: &CapabilityGroup{Name: item.Name, Description: item.Description, Optional: item.Optional, Capabilities: []Capability{member}}, Source: source}
			} else {
				g := *item.Group
				g.Capabilities = append([]Capability(nil), g.Capabilities...)
				item.Group = &g
				item.Source = source
				for j := range g.Capabilities {
					if g.Capabilities[j].Source == nil {
						g.Capabilities[j].Source = &CapabilitySource{Kit: source.Kit, Path: fmt.Sprintf("%s.group.capabilities[%d]", source.Path, j)}
					} else {
						memberSource := *g.Capabilities[j].Source
						if memberSource.Kit == "" {
							memberSource.Kit = contribution.Reference
						}
						g.Capabilities[j].Source = &memberSource
					}
				}
			}
			for j, member := range item.Group.Capabilities {
				if member.Type != CapabilityAgentContext {
					continue
				}
				var ac AgentContext
				if err := DecodeCapabilityConfig(member, &ac); err != nil {
					return nil, err
				}
				// Keep runtime templates in the descriptor: staged file bodies
				// are not evaluated by descriptor expansion.
				if ContainsEnvRef(ac.Content) || (ac.ContentFile == "" && ac.Content == "") {
					continue
				}
				if opts.ContextPath == "" {
					return nil, fmt.Errorf("merge: conditional agent-context requires ContextPath")
				}
				target := fmt.Sprintf("%s.parts/%d.md", opts.ContextPath, count)
				count++
				result.ContextSources = append(result.ContextSources, ContextSource{Reference: contribution.Reference, Path: ac.ContentFile, Content: ac.Content, Target: target})
				ac.Content = ""
				ac.ContentFile = target
				updated, err := CapabilityWithConfig(member, ac)
				if err != nil {
					return nil, err
				}
				item.Group.Capabilities[j] = *updated
			}
			result.Descriptor.Capabilities = append(result.Descriptor.Capabilities, item)
		}
	}
	return result, nil
}
