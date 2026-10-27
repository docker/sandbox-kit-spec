package spec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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

// SelectionTarget identifies a member in the consumed descriptor, independently
// of publisher-provided CapabilitySource attribution and selection outcomes.
// It is not serialized into the descriptor or taken from publisher metadata.
type SelectionTarget struct {
	// Reference is supplied to SelectCapabilities by the caller. Fetch APIs use
	// the consumption reference, not a resolved digest or built image reference.
	Reference string
	// Path is the zero-based declaration path before selection or flattening,
	// matching SelectionRecord.Members: capabilities[i] for an ordinary entry,
	// or capabilities[i].group.capabilities[j] for a group member.
	Path string
}

// SelectCapability answers whether the runtime will provide one expanded entry.
// The target identifies that member without depending on callback order or
// shared descriptor storage. Capability.Source remains diagnostic metadata.
// The descriptor is the owning Kit after argument and environment expansion,
// including its DisplayName and all declarations before selection. Target,
// descriptor, and capability inputs are passed by value. The context carries
// cancellation and deadlines.
// It must not apply the entry: the enclosing group can still be rejected.
type SelectCapability func(context.Context, SelectionTarget, Descriptor, Capability) CapabilityDecision

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
	return func(_ context.Context, _ SelectionTarget, _ Descriptor, c Capability) CapabilityDecision {
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
		CapabilityPrivileged, CapabilityLifecycle, CapabilityAgentContext, CapabilityAgentContextV2, CapabilityAgentSessions, CapabilityAgentInteractiveSessions, CapabilityACP,
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
	hasBlocks := false
	for _, item := range d.Capabilities {
		hasBlocks = hasBlocks || len(item.DeclarationBlocks) > 0
	}
	if !HasGroups(d.Capabilities) && !hasBlocks {
		return validateCapabilityBlock(d)
	}
	var errs ValidationErrors
	// Each entry is judged once, however many blocks it joins: its group
	// and its blocks compare only what that judgment found, so a large
	// config costs the same in one block as in many.
	type block struct {
		members []Capability
		checks  []*entryCheck
		paths   []string
	}
	blocks := map[string]*block{}
	var blockOrder []string
	addBlock := func(id string, member Capability, check *entryCheck, path string) {
		if blocks[id] == nil {
			blocks[id] = &block{}
			blockOrder = append(blockOrder, id)
		}
		blocks[id].members = append(blocks[id].members, member)
		blocks[id].checks = append(blocks[id].checks, check)
		blocks[id].paths = append(blocks[id].paths, path)
	}
	for i, c := range d.Capabilities {
		at := fmt.Sprintf("capabilities[%d]", i)
		if c.Source != nil && c.Source.Path == "" {
			errs.add(fieldErrorf(at+".source", "path is required"))
		}
		var memberChecks []*entryCheck
		var memberPaths []string
		if c.Group != nil {
			memberChecks = make([]*entryCheck, len(c.Group.Capabilities))
			memberPaths = make([]string, len(c.Group.Capabilities))
			for j, member := range c.Group.Capabilities {
				// Judged as a block's first entry, then remapped to its
				// own path alone.
				check := checkEntry(d.Kind, 0, member)
				memberChecks[j] = &check
				memberPaths[j] = fmt.Sprintf("%s.group.capabilities[%d]", at, j)
			}
		}
		var joined []string
		seenBlocks := map[string]bool{}
		for j, id := range c.DeclarationBlocks {
			if id == "" || seenBlocks[id] {
				errs.add(fieldErrorf(fmt.Sprintf("%s.declarationBlocks[%d]", at, j), "block id must be nonempty and unique"))
				continue
			}
			seenBlocks[id] = true
			if c.Group != nil {
				if len(c.Group.Capabilities) != 1 {
					errs.add(fieldErrorf(at+".declarationBlocks", "only ordinary entries and one-member groups may carry declaration blocks"))
					continue
				}
				addBlock(id, c.Group.Capabilities[0], memberChecks[0], memberPaths[0])
				continue
			}
			joined = append(joined, id)
		}
		if c.Group == nil {
			if !c.groupSet && len(c.DeclarationBlocks) == 0 {
				joined = append(joined, "")
			}
			// An entry in no block, such as one whose every block id is
			// refused, is not judged, as before.
			if len(joined) > 0 {
				check := checkEntry(d.Kind, i, c)
				errs.add(check.errs.err())
				for _, id := range joined {
					addBlock(id, c, &check, at)
				}
			}
			if c.groupSet {
				errs.add(fieldErrorf(at+".group", "group must be an object"))
			}
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
		for j, member := range cp.Capabilities {
			if member.Group != nil || member.groupSet {
				errs.add(fieldErrorf(memberPaths[j], "nested groups are not permitted"))
			}
			if len(member.DeclarationBlocks) > 0 || member.declarationBlocksSet {
				errs.add(fieldErrorf(memberPaths[j]+".declarationBlocks", "group members must not state declaration blocks"))
			}
			if member.Optional || member.optionalSet {
				errs.add(fieldErrorf(memberPaths[j]+".optional", "group members must not state optional"))
			}
		}
		for j, check := range memberChecks {
			errs.add(remapCapabilityErrors(check.errs.err(), memberPaths[j:j+1]))
		}
		errs.add(remapCapabilityErrors(compareCapabilityBlock(&cp, memberChecks), memberPaths))
	}
	for _, id := range blockOrder {
		cp := *d
		cp.Capabilities = blocks[id].members
		cp.declarationsOnly = true
		errs.add(remapCapabilityErrors(compareCapabilityBlock(&cp, blocks[id].checks), blocks[id].paths))
	}
	return errs.err()
}

func remapCapabilityErrors(err error, paths []string) error {
	if err == nil {
		return nil
	}
	// One replacer serves every error from the block, so remapping costs
	// the errors plus the paths, not their product.
	replacements := make([]string, 0, 2*len(paths))
	for i, to := range paths {
		replacements = append(replacements, fmt.Sprintf("capabilities[%d]", i), to)
	}
	return remapWith(err, paths, strings.NewReplacer(replacements...))
}

func remapWith(err error, paths []string, replacer *strings.Replacer) error {
	if all, ok := err.(ValidationErrors); ok {
		out := make(ValidationErrors, len(all))
		for i, e := range all {
			out[i] = remapWith(e, paths, replacer)
		}
		return out
	}
	if field, ok := err.(*FieldError); ok {
		if i, rest, ok := capabilityIndex(field.Path); ok && i < len(paths) {
			return &FieldError{Path: paths[i] + rest, err: &capabilityPathError{cause: field.err, paths: replacer}}
		}
	}
	return err
}

// capabilityIndex reads the entry index a block-local path starts with,
// "capabilities[i]" alone or followed by a field.
func capabilityIndex(path string) (int, string, bool) {
	rest, ok := strings.CutPrefix(path, "capabilities[")
	if !ok {
		return 0, "", false
	}
	digits, rest, ok := strings.Cut(rest, "]")
	if !ok || digits == "" || (rest != "" && !strings.HasPrefix(rest, ".")) {
		return 0, "", false
	}
	i, err := strconv.Atoi(digits)
	if err != nil || i < 0 || strconv.Itoa(i) != digits {
		return 0, "", false
	}
	return i, rest, true
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
// Reference is the caller's consumption identity, passed unchanged in each
// SelectionTarget. It may be empty for standalone descriptors without a reference.
// A nil descriptor or selector is an error, never an implicit default.
// Required rejection returns records alongside the error for diagnostics.
// Cancellation returns a zero selection and the context error.
func SelectCapabilities(ctx context.Context, reference string, d *Descriptor, selectCapability SelectCapability) (Selection, error) {
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
		if argRef.Match(raw) || len(capabilityArgRefs([]Capability{item})) > 0 {
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
			decision := selectCapability(ctx, SelectionTarget{Reference: reference, Path: memberPath}, *d, c)
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
			// All original blocks validated before selection. Effective
			// grants no longer carry publication-only block membership.
			c.DeclarationBlocks = nil
			c.declarationBlocksSet = false
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
			// Inline guidance stays with its original selectable declaration.
			// Combining it now would couple optional bodies to profile selection
			// and duplicate shared guidance when this set is composed again.
			if item.Type == CapabilityAgentContextV2 {
				return true
			}
			// A set may re-export storage destinations or settings. Keep
			// their requests separate until create expands the values:
			// instance keys and equivalence cannot judge placeholders.
			if (item.Type == CapabilityVolume || item.Type == CapabilityHostMount) && capabilityIsParameterized(item) {
				return true
			}
			// A port's family is reconciled, not keyed, so a placeholder
			// shares a slot with a stated family it cannot yet be compared
			// with, and collapsing them would drop one of the constraints.
			if item.Type == CapabilityPort {
				if family, ok := item.Config["family"].(string); ok && ContainsArgRef(family) {
					return true
				}
			}
			// Any type's key is judged the same way once it holds a
			// reference; references beside a literal key still reconcile.
			if keyHoldsReference(item) {
				return true
			}
		}
	}
	return false
}

// refuseConcreteConflicts holds the requests a deferred publication leaves
// apart to the rules an undeferred merge applies, where they are already
// concrete: an unrelated deferred request cannot hide a conflict between
// them. A required group's member, as a nested set's required requests
// arrive, is asked for as surely as an ungrouped entry. An optional group
// may be declined in favor of another the same kit offers for the same
// key, so its members wait for selection, as agent-context bodies wait
// for staging. A request holding a reference waits for create, except
// what its literal keys already settle: a credential or host mount has one
// owner whatever its config, so each literal key, a credential's per
// phase, refuses a second asker, and a volume's literal path is a
// destination a host mount owns alone, whatever the volume's settings.
func refuseConcreteConflicts(contributions []Contribution) error {
	type claim struct {
		key       InstanceKey
		reference string
	}
	var hostMounts, volumes []claim
	concrete := make([]Contribution, len(contributions))
	for i, c := range contributions {
		d := &Descriptor{SchemaVersion: c.Descriptor.SchemaVersion, Kind: c.Descriptor.Kind}
		check := func(item Capability, source CapabilitySource) {
			if item.Type == CapabilityAgentContext || item.Type == CapabilityAgentContextV2 {
				return
			}
			if item.Source != nil {
				source = *item.Source
				if source.Kit == "" {
					source.Kit = c.Reference
				}
			}
			item.Source = &source
			reference := fmt.Sprintf("%s (%s %s)", c.Reference, source.Kit, source.Path)
			parameterized := capabilityIsParameterized(item)
			switch {
			case !parameterized:
				d.Capabilities = append(d.Capabilities, item)
			case item.Type == CapabilityCredential || item.Type == CapabilityHostMount:
				for _, config := range literalKeyConfigs(item) {
					standIn := item
					standIn.Config = config
					d.Capabilities = append(d.Capabilities, standIn)
				}
			case item.Type == CapabilityVolume:
				for _, key := range literalInstanceKeys(item) {
					volumes = append(volumes, claim{key, reference})
				}
				return
			}
			if item.Type == CapabilityHostMount {
				for _, key := range literalInstanceKeys(item) {
					hostMounts = append(hostMounts, claim{key, reference})
				}
			}
		}
		for j, item := range c.Descriptor.Capabilities {
			source := CapabilitySource{Kit: c.Reference, Path: fmt.Sprintf("capabilities[%d]", j)}
			if item.Group == nil {
				check(item, source)
				continue
			}
			if item.Group.Optional {
				continue
			}
			if item.Source != nil {
				source.Path = item.Source.Path
			}
			for k, member := range item.Group.Capabilities {
				check(member, CapabilitySource{Kit: c.Reference, Path: fmt.Sprintf("%s.group.capabilities[%d]", source.Path, k)})
			}
		}
		concrete[i] = Contribution{Reference: c.Reference, Descriptor: d}
	}
	if _, _, err := mergeCapabilities(concrete); err != nil {
		return err
	}
	for _, volume := range volumes {
		for _, mount := range hostMounts {
			if volume.key.identity() == mount.key.identity() {
				return fmt.Errorf("merge: %s and %s both declare storage at the same path; a host mount has one owner", mount.reference, volume.reference)
			}
		}
	}
	return nil
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
	if err := refuseConcreteConflicts(contributions); err != nil {
		return nil, err
	}
	count := 0
	storage := capabilityMerge{byKey: map[string]keyed{}, optional: map[string]bool{}}
	storagePositions := map[string]int{}
	for contributionIndex, contribution := range contributions {
		// Remap descriptor-local IDs, even when a set includes the same
		// reference twice. Attribution cannot provide structural identity.
		blockIDs := map[string]string{}
		remapBlock := func(id string) string {
			if mapped, ok := blockIDs[id]; ok {
				return mapped
			}
			mapped := fmt.Sprintf("%d:%d", contributionIndex, len(blockIDs))
			blockIDs[id] = mapped
			return mapped
		}
		for i, item := range contribution.Descriptor.Capabilities {
			ids := item.DeclarationBlocks
			item.DeclarationBlocks = nil
			if len(ids) == 0 && item.Group == nil {
				ids = []string{""}
			}
			for _, id := range ids {
				item.DeclarationBlocks = append(item.DeclarationBlocks, remapBlock(id))
			}
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
					merged.DeclarationBlocks = append(append([]string(nil), result.Descriptor.Capabilities[position].DeclarationBlocks...), item.DeclarationBlocks...)
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
				member.DeclarationBlocks = nil
				member.declarationBlocksSet = false
				item = Capability{DeclarationBlocks: item.DeclarationBlocks, Group: &CapabilityGroup{Name: item.Name, Description: item.Description, Optional: item.Optional, Capabilities: []Capability{member}}, Source: source}
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
