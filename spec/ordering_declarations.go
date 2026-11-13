package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

func stringSequenceYAML(n yaml.Node, field string) error {
	if n.Kind == yaml.AliasNode {
		n = *n.Alias
	}
	if n.ShortTag() != "!!seq" {
		return fmt.Errorf("%s must be an array of strings", field)
	}
	for _, value := range n.Content {
		if value.ShortTag() != "!!str" {
			return fmt.Errorf("%s must contain strings", field)
		}
	}
	return nil
}

func stringSequenceJSON(raw json.RawMessage, field string) error {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("%s must be an array of strings: %w", field, err)
	}
	if values == nil {
		return fmt.Errorf("%s must be an array of strings", field)
	}
	for _, raw := range values {
		var value string
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("%s must contain strings", field)
		}
	}
	return nil
}

func (d *Descriptor) UnmarshalYAML(unmarshal func(any) error) error {
	type plain Descriptor
	if err := unmarshal((*plain)(d)); err != nil {
		return err
	}
	var fields map[string]yaml.Node
	if err := unmarshal(&fields); err != nil {
		return err
	}
	if n, stated := fields["capabilityScopes"]; stated {
		if n.Kind == yaml.AliasNode {
			n = *n.Alias
		}
		if n.ShortTag() != "!!seq" {
			return fmt.Errorf("capabilityScopes must be an array of objects")
		}
		for _, scope := range n.Content {
			if scope.ShortTag() != "!!map" {
				return fmt.Errorf("capabilityScopes must contain objects")
			}
		}
	}
	return nil
}

func (d *Descriptor) UnmarshalJSON(data []byte) error {
	type plain Descriptor
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode((*plain)(d)); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, stated := fields["capabilityScopes"]; stated {
		var scopes []json.RawMessage
		if err := json.Unmarshal(raw, &scopes); err != nil || scopes == nil {
			return fmt.Errorf("capabilityScopes must be an array of objects")
		}
		for _, scope := range scopes {
			if bytes.Equal(bytes.TrimSpace(scope), []byte("null")) {
				return fmt.Errorf("capabilityScopes must contain objects")
			}
		}
	}
	return nil
}

func (s *CapabilityScope) UnmarshalYAML(unmarshal func(any) error) error {
	type plain CapabilityScope
	if err := unmarshal((*plain)(s)); err != nil {
		return err
	}
	var fields map[string]yaml.Node
	if err := unmarshal(&fields); err != nil {
		return err
	}
	for _, field := range []string{"provides", "requires", "integrates"} {
		if value, stated := fields[field]; stated {
			if err := stringSequenceYAML(value, "capability scope "+field); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *CapabilityScope) UnmarshalJSON(data []byte) error {
	type plain CapabilityScope
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode((*plain)(s)); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, field := range []string{"provides", "requires", "integrates"} {
		if value, stated := fields[field]; stated {
			if err := stringSequenceJSON(value, "capability scope "+field); err != nil {
				return err
			}
		}
	}
	return nil
}

// orderingTarget separates the four authored forms without inventing IDs from
// array positions. A local ID has local=true; a qualified ID combines provider
// and id. An exact type includes its version, unlike an unversioned provider.
type orderingTarget struct {
	capabilityType string
	provider       string
	id             string
	local          bool
}

func parseOrderingTarget(raw string) (orderingTarget, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return orderingTarget{}, fmt.Errorf("ordering target must be a literal type, provided name, or ID reference")
	}
	if needType.MatchString(raw) {
		return orderingTarget{capabilityType: raw}, nil
	}
	provider, id, qualified := strings.Cut(raw, "#")
	if qualified && !capabilityName.MatchString(id) {
		return orderingTarget{}, fmt.Errorf("invalid ordering ID in %q", raw)
	}
	if qualified && provider == "" {
		return orderingTarget{id: id, local: true}, nil
	}
	if !validCapabilityName(provider) {
		return orderingTarget{}, fmt.Errorf("invalid ordering target %q", raw)
	}
	return orderingTarget{provider: NormalizeCapabilityName(provider), id: id}, nil
}

// validateOrderingDeclarations checks facts available within one descriptor.
// Local IDs resolve here, including members of optional groups. Cross-Kit
// provider and type targets wait for the full runtime graph, since an absent
// target in this descriptor may be supplied by another contribution.
func validateOrderingDeclarations(d *Descriptor) error {
	var errs ValidationErrors
	if d.CapabilityScopes != nil && len(d.CapabilityScopes) == 0 {
		errs.add(fieldErrorf("capabilityScopes", "scope table must not be empty"))
	}
	provided := map[string]bool{}
	for _, raw := range d.Provides {
		if p, err := ParseProvide(raw); err == nil {
			provided[p.Name] = true
		}
	}
	for i, scope := range d.CapabilityScopes {
		for _, dependency := range []struct {
			name   string
			values []string
		}{{"requires", scope.Requires}, {"integrates", scope.Integrates}} {
			for j, raw := range dependency.values {
				if _, err := ParseRequire(raw); err != nil {
					errs.add(fieldErrorf(fmt.Sprintf("capabilityScopes[%d].%s[%d]", i, dependency.name, j), "%v", err))
				}
			}
		}
		seen := map[string]bool{}
		for j, name := range scope.Provides {
			at := fmt.Sprintf("capabilityScopes[%d].provides[%d]", i, j)
			if !validCapabilityName(name) || NormalizeCapabilityName(name) != name {
				errs.add(fieldErrorf(at, "scope provides must be normalized unversioned names"))
			} else if !provided[name] {
				errs.add(fieldErrorf(at, "scope name %q is absent from descriptor provides", name))
			}
			if seen[name] {
				errs.add(fieldErrorf(at, "scope provides repeats %q", name))
			}
			seen[name] = true
		}
	}
	type declaration struct {
		capability Capability
		path       string
		scope      uint32
	}
	var entries []declaration
	ids := map[uint32]map[string]int{}
	add := func(c Capability, at string) {
		scope := uint32(0)
		if d.CapabilityScopes == nil {
			if c.Scope != nil {
				errs.add(fieldErrorf(at+".scope", "scope requires capabilityScopes"))
			}
		} else if c.Scope == nil || uint64(*c.Scope) >= uint64(len(d.CapabilityScopes)) {
			errs.add(fieldErrorf(at+".scope", "every declaration must reference a valid capabilityScopes index"))
		} else {
			scope = *c.Scope
		}
		if c.ID != "" {
			if !capabilityName.MatchString(c.ID) {
				errs.add(fieldErrorf(at+".id", "id must be a literal unqualified capability name"))
			}
			if ids[scope] == nil {
				ids[scope] = map[string]int{}
			}
			if prev, exists := ids[scope][c.ID]; exists {
				errs.add(fieldErrorf(at+".id", "id %q already declared at %s in this Kit scope", c.ID, entries[prev].path))
			} else {
				ids[scope][c.ID] = len(entries)
			}
		}
		entries = append(entries, declaration{c, at, scope})
	}
	for i, item := range d.Capabilities {
		at := fmt.Sprintf("capabilities[%d]", i)
		if item.Group == nil {
			add(item, at)
			continue
		}
		if item.ID != "" || item.Before != nil || item.After != nil || item.Scope != nil {
			errs.add(fieldErrorf(at, "group wrappers must not carry id, before, after, or scope"))
		}
		for j, member := range item.Group.Capabilities {
			add(member, fmt.Sprintf("%s.group.capabilities[%d]", at, j))
		}
	}
	for i, entry := range entries {
		for _, relation := range []struct {
			name   string
			values []string
		}{{"before", entry.capability.Before}, {"after", entry.capability.After}} {
			for j, raw := range relation.values {
				at := fmt.Sprintf("%s.%s[%d]", entry.path, relation.name, j)
				target, err := parseOrderingTarget(raw)
				if err != nil {
					errs.add(fieldErrorf(at, "%v", err))
					continue
				}
				if !target.local {
					continue
				}
				to, found := ids[entry.scope][target.id]
				if !found {
					errs.add(fieldErrorf(at, "local ordering ID %q is not declared", target.id))
					continue
				}
				if i == to {
					errs.add(fieldErrorf(at, "explicit ordering self-reference %q", raw))
					continue
				}
			}
		}
	}
	// A cycle matters only where its entries share a phase and step. Authoring
	// validation does not know third-party contracts or the eventual selection;
	// runtime composition checks cycles after both are known.
	return errs.err()
}

func unionExecutionOrigins(groups ...[]int) []int {
	var out []int
	for _, group := range groups {
		out = append(out, group...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// publicationScopes materializes implicit and aggregate provider bindings before
// this descriptor is nested in another set. An unbound aggregate name targets
// all of this artifact's scopes. Binding it now prevents that alias from later
// expanding to unrelated sibling Kits in the enclosing publication.
func publicationScopes(d *Descriptor, remaining *int) ([]CapabilityScope, error) {
	tooLarge := fmt.Errorf("merge: repeated scope metadata exceeds the %d byte budget: %w", SizeErrorBytes, ErrDescriptorTooLarge)
	// Count a lower bound of the eventual JSON before copying repeated data.
	// This budget is shared across contributions. Braces and encoded strings
	// survive publication unchanged; the final descriptor check also accounts
	// for field names, separators and all other descriptor metadata.
	scopeCount := max(len(d.CapabilityScopes), 1)
	if scopeCount > *remaining/2 {
		return nil, tooLarge
	}
	*remaining -= 2 * scopeCount
	charge := func(value string, copies int) error {
		// Divide first so multiplication cannot overflow, and so a large input
		// string is refused before its JSON-escaped copy is allocated.
		if len(value) > *remaining/copies {
			return tooLarge
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("merge: serialize scope metadata: %w", err)
		}
		if len(encoded) > *remaining/copies {
			return tooLarge
		}
		*remaining -= len(encoded) * copies
		return nil
	}
	scopes := make([]CapabilityScope, scopeCount)
	bound := map[string]bool{}
	for i, scope := range d.CapabilityScopes {
		for _, values := range [][]string{scope.Provides, scope.Requires, scope.Integrates} {
			for _, value := range values {
				if err := charge(value, 1); err != nil {
					return nil, err
				}
			}
		}
		scopes[i].Provides = slices.Clone(scope.Provides)
		scopes[i].Requires = slices.Clone(scope.Requires)
		scopes[i].Integrates = slices.Clone(scope.Integrates)
		for _, name := range scope.Provides {
			bound[name] = true
		}
	}
	for _, raw := range d.Provides {
		p, err := ParseProvide(raw)
		if err != nil {
			return nil, err
		}
		if bound[p.Name] {
			continue
		}
		// One alias can bind every original scope. Refuse that whole fan-out
		// before the first append instead of discovering it after allocation.
		if err := charge(p.Name, len(scopes)); err != nil {
			return nil, err
		}
		for i := range scopes {
			scopes[i].Provides = append(scopes[i].Provides, p.Name)
		}
		bound[p.Name] = true
	}
	// Aggregate external dependencies apply to the artifact as a whole. Keep
	// them alongside original scope dependencies so nesting cannot erase an
	// ordering constraint that was visible before publication.
	for i := range scopes {
		for _, raw := range d.Requires {
			if !slices.Contains(scopes[i].Requires, raw) {
				if err := charge(raw, 1); err != nil {
					return nil, err
				}
				scopes[i].Requires = append(scopes[i].Requires, raw)
			}
		}
		for _, raw := range d.Integrates {
			if !slices.Contains(scopes[i].Integrates, raw) {
				if err := charge(raw, 1); err != nil {
					return nil, err
				}
				scopes[i].Integrates = append(scopes[i].Integrates, raw)
			}
		}
	}
	return scopes, nil
}
