// Command check exhaustively checks the finite selection domain documented in
// the companion skill against the public Go API, including cancellation.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

const (
	maxItems   = 3
	maxMembers = 3
	reference  = "consumed-kit:1.0.0"
)

type item struct {
	group    bool
	optional bool
	members  int
	accept   int
}

func (it item) String() string {
	kind := "ordinary"
	if it.group {
		kind = "group"
	}
	var decisions []string
	for j := range it.members {
		decision := "reject"
		if it.accept&(1<<j) != 0 {
			decision = "accept"
		}
		decisions = append(decisions, decision)
	}
	return fmt.Sprintf("%s(optional=%t,%s)", kind, it.optional, strings.Join(decisions, ","))
}

type example struct {
	descriptor spec.Descriptor
	targets    []spec.SelectionTarget
	decisions  []spec.CapabilityDecision
	types      []string
	want       spec.Selection
	required   []string
}

func describe(items []item) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = it.String()
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

// Expected grants are the concatenation of wholly accepted items. The oracle
// depends on the generated decisions, never on the implementation's records.
func build(items []item) example {
	ex := example{descriptor: spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload, DisplayName: "same label"}}
	for i, it := range items {
		at := fmt.Sprintf("capabilities[%d]", i)
		source := &spec.CapabilitySource{Kit: "publisher", Path: "capabilities[99]"}
		record := spec.SelectionRecord{Path: at, Name: "same label", Source: source}
		var members []spec.Capability
		for j := range it.members {
			path := at
			if it.group {
				path = fmt.Sprintf("%s.group.capabilities[%d]", at, j)
			}
			member := spec.Capability{Type: fmt.Sprintf("com.example/member-%d-%d@1", i, j), Name: "same label", Source: source}
			decision := spec.CapabilityDecision{Accepted: it.accept&(1<<j) != 0, Message: fmt.Sprintf("decision-%d-%d", i, j)}
			members = append(members, member)
			ex.targets = append(ex.targets, spec.SelectionTarget{Reference: reference, Path: path})
			ex.types = append(ex.types, member.Type)
			ex.decisions = append(ex.decisions, decision)
			record.Members = append(record.Members, path)
			record.MemberSources = append(record.MemberSources, *source)
			record.Decisions = append(record.Decisions, decision)
			if !decision.Accepted {
				record.Rejected = append(record.Rejected, path)
				if !it.optional {
					ex.required = append(ex.required, path, decision.Message)
				}
			}
		}
		var declaration spec.Capability
		if it.group {
			declaration = spec.Capability{Source: source, Group: &spec.CapabilityGroup{Name: "same label", Optional: it.optional, Capabilities: members}}
		} else {
			members[0].Optional = it.optional
			declaration = members[0]
		}
		ex.descriptor.Capabilities = append(ex.descriptor.Capabilities, declaration)
		if it.accept == (1<<it.members)-1 {
			ex.want.Capabilities = append(ex.want.Capabilities, members...)
			ex.want.Selected = append(ex.want.Selected, record)
		} else {
			ex.want.Skipped = append(ex.want.Skipped, record)
		}
	}
	return ex
}

func violation(property, detail string) error {
	return fmt.Errorf("COUNTEREXAMPLE [%s]: %s", property, detail)
}

func granted(selection spec.Selection) []string {
	var types []string
	for _, c := range selection.Capabilities {
		types = append(types, c.Type)
	}
	return types
}

// cancelAfter=-1 runs to completion, 0 cancels before entry, and k>0
// cancels inside callback k. No callback mutates the descriptor or its inputs.
func check(ex example, cancelAfter int) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if cancelAfter == 0 {
		cancel()
	}
	calls := 0
	var callbackError error
	selection, err := spec.SelectCapabilities(ctx, reference, &ex.descriptor,
		func(gotCtx context.Context, target spec.SelectionTarget, d spec.Descriptor, c spec.Capability) spec.CapabilityDecision {
			if calls >= len(ex.targets) {
				callbackError = violation("target-identity", "selector called more than once per member")
				return spec.CapabilityDecision{}
			}
			if gotCtx != ctx || target != ex.targets[calls] || c.Type != ex.types[calls] || !reflect.DeepEqual(d, ex.descriptor) {
				callbackError = violation("target-identity", fmt.Sprintf("callback %d got target %+v and type %s; want %+v and type %s", calls+1, target, c.Type, ex.targets[calls], ex.types[calls]))
			}
			decision := ex.decisions[calls]
			calls++
			if calls == cancelAfter {
				cancel()
			}
			return decision
		})
	if callbackError != nil {
		return callbackError
	}
	if cancelAfter >= 0 {
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(selection, spec.Selection{}) || calls != cancelAfter {
			return violation("cancellation", fmt.Sprintf("cancel after callback %d: got %d calls, selection %+v, error %v; want no partial selection and context.Canceled", cancelAfter, calls, selection, err))
		}
		return nil
	}
	if calls != len(ex.targets) {
		return violation("target-identity", fmt.Sprintf("got %d callbacks, want %d", calls, len(ex.targets)))
	}
	if (err != nil) != (len(ex.required) > 0) {
		return violation("required-rejection", fmt.Sprintf("error = %v; required rejection evidence = %v", err, ex.required))
	}
	for _, evidence := range ex.required {
		if !strings.Contains(err.Error(), evidence) {
			return violation("required-rejection", fmt.Sprintf("error %q omits %q", err, evidence))
		}
	}
	got, want := granted(selection), granted(ex.want)
	gotSet, wantSet := slices.Clone(got), slices.Clone(want)
	slices.Sort(gotSet)
	slices.Sort(wantSet)
	if !slices.Equal(gotSet, wantSet) {
		return violation("atomic-selection", fmt.Sprintf("granted %v; want exactly %v", got, want))
	}
	if !slices.Equal(got, want) {
		return violation("declaration-order", fmt.Sprintf("granted order %v; want %v", got, want))
	}
	if !reflect.DeepEqual(selection, ex.want) {
		return violation("decision-records", fmt.Sprintf("selection %+v differs from expected %+v", selection, ex.want))
	}
	return nil
}

func verify() error {
	var variants []item
	for _, group := range []bool{false, true} {
		limit := 1
		if group {
			limit = maxMembers
		}
		for members := 1; members <= limit; members++ {
			for _, optional := range []bool{false, true} {
				for accept := range 1 << members {
					variants = append(variants, item{group: group, optional: optional, members: members, accept: accept})
				}
			}
		}
	}
	completions, cancellations := 0, 0
	var walk func([]item, int) error
	walk = func(items []item, remaining int) error {
		if remaining > 0 {
			for _, it := range variants {
				if err := walk(append(items, it), remaining-1); err != nil {
					return err
				}
			}
			return nil
		}
		ex := build(items)
		if err := check(ex, -1); err != nil {
			return fmt.Errorf("%w\n  declarations: %s\n  run: completion", err, describe(items))
		}
		completions++
		for cancelAfter := 0; cancelAfter <= len(ex.targets); cancelAfter++ {
			if err := check(ex, cancelAfter); err != nil {
				return fmt.Errorf("%w\n  declarations: %s\n  run: cancel after callback %d", err, describe(items), cancelAfter)
			}
			cancellations++
		}
		return nil
	}
	for n := 0; n <= maxItems; n++ {
		if err := walk(nil, n); err != nil {
			return err
		}
	}
	fmt.Printf("PASS: %d completion cases; %d cancellation cases; up to %d items and %d members per group\n", completions, cancellations, maxItems, maxMembers)
	return nil
}

func main() {
	if err := verify(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
