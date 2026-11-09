package spec

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func orderPointer(value int32) *int32 { return &value }

func TestCapabilityOrderGrammar(t *testing.T) {
	for _, value := range []int32{-2147483648, -1, 0, 1, 2147483647} {
		raw := []byte(fmt.Sprintf("schemaVersion: '3'\nkind: mixin\ncapabilities:\n  - type: example.com/action@1\n    order: %d\n", value))
		d, err := Decode(raw)
		require.NoError(t, err)
		require.Equal(t, orderPointer(value), d.Capabilities[0].Order)
		encoded, err := json.Marshal(d)
		require.NoError(t, err)
		var roundtrip Descriptor
		require.NoError(t, json.Unmarshal(encoded, &roundtrip))
		require.Equal(t, d.Capabilities[0].Order, roundtrip.Capabilities[0].Order)
	}
	for _, value := range []string{"null", "1.5", "1.0", "'12'", "true", "2147483648", "-2147483649", "${{kit.args.order}}"} {
		raw := []byte("schemaVersion: '3'\nkind: mixin\ncapabilities:\n  - type: example.com/action@1\n    order: " + value + "\n")
		_, err := Decode(raw)
		require.Error(t, err, value)
	}
	for _, value := range []string{"null", "1.5", "1.0", `"12"`, "true", "2147483648", "-2147483649"} {
		var entry Capability
		require.Error(t, json.Unmarshal([]byte(`{"type":"example.com/action@1","order":`+value+`}`), &entry), value)
	}
	for _, raw := range []string{
		`{"group":{"capabilities":[{"type":"example.com/action@1"}]},"order":0}`,
		"group: {capabilities: [{type: example.com/action@1}]}\norder: 0",
	} {
		_, err := Decode([]byte("schemaVersion: '3'\nkind: mixin\ncapabilities:\n  - " + raw))
		if raw[0] == '{' {
			var entry Capability
			err = json.Unmarshal([]byte(raw), &entry)
		}
		require.Error(t, err)
	}
	_, err := Validate(&Descriptor{Kind: KindMixin, Capabilities: []Capability{{Order: orderPointer(0), Group: &CapabilityGroup{Capabilities: []Capability{{Type: "example.com/action@1"}}}}}})
	require.Error(t, err)
	entry := Capability{Type: "example.com/action@1", Order: orderPointer(0)}
	raw, err := json.Marshal(entry)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"order":0`)
	entry.Order = nil
	raw, err = json.Marshal(entry)
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"order"`)
}

func TestCapabilityOrderSchemaBounds(t *testing.T) {
	order := at(t, loadSchema(t), "definitions", "capability", "properties", "order")
	require.Equal(t, "integer", order["type"])
	require.Equal(t, float64(-2147483648), order["minimum"])
	require.Equal(t, float64(2147483647), order["maximum"])
}

func TestLifecycleRepeatedEntriesPreserveBoundaries(t *testing.T) {
	first, second := groupHook("same"), groupHook("same")
	first.Order, second.Order = orderPointer(300), orderPointer(100)
	first.Optional, first.Name, second.Name = true, "first", "second"
	d := &Descriptor{Kind: KindMixin, SchemaVersion: SchemaVersion, Capabilities: []Capability{first, second}}
	_, err := Validate(d)
	require.NoError(t, err)
	inputs := []Contribution{{Reference: "kit", Descriptor: d}}
	for _, publish := range []bool{false, true} {
		var effective *Descriptor
		if publish {
			result, err := Merge(inputs, MergeOptions{})
			require.NoError(t, err)
			raw, err := json.Marshal(result.Descriptor)
			require.NoError(t, err)
			effective, err = Decode(raw)
			require.NoError(t, err)
		} else {
			effective, err = Compose(inputs)
			require.NoError(t, err)
		}
		require.Len(t, effective.Capabilities, 2)
		require.Equal(t, first.Order, effective.Capabilities[0].Order)
		require.Equal(t, second.Order, effective.Capabilities[1].Order)
		require.True(t, effective.Capabilities[0].Optional)
		require.False(t, effective.Capabilities[1].Optional)
		_, err := LifecycleOf(effective.Capabilities)
		require.ErrorContains(t, err, "multiple lifecycle")
		entries, err := LifecyclesOf(effective.Capabilities)
		require.NoError(t, err)
		require.Len(t, entries, 2)
	}
	second.Order = first.Order
	d.Capabilities = []Capability{second, second}
	_, err = Validate(d)
	require.NoError(t, err, "identical lifecycle hook entries remain independent commands")
}

func TestLifecycleCrossEntryConflictsIgnoreOrder(t *testing.T) {
	for _, field := range []string{"files", "interactive"} {
		first, second := groupHook("one"), groupHook("two")
		first.Order, second.Order = orderPointer(100), orderPointer(200)
		if field == "files" {
			first.Config[field] = []any{map[string]any{"path": "/tmp/file", "content": "one"}}
			second.Config[field] = []any{map[string]any{"path": "/tmp/./file", "content": "two"}}
		} else {
			first.Config[field], second.Config[field] = []string{}, []string{}
		}
		d := &Descriptor{Kind: KindMixin, Capabilities: []Capability{first, second}}
		_, err := Validate(d)
		require.ErrorContains(t, err, "capabilities[0]")
		require.ErrorContains(t, err, "capabilities[1]")
		_, err = Compose([]Contribution{{Reference: "one", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{first}}}, {Reference: "two", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{second}}}})
		require.Error(t, err)
	}
}

func TestReconciledCapabilityOrdersMustAgree(t *testing.T) {
	for _, entry := range []Capability{
		{Type: CapabilityPrivileged},
		{Type: CapabilityDockerd},
		{Type: CapabilityNetworkPolicy},
		{Type: CapabilityVolume, Config: map[string]any{"path": "/cache"}},
		{Type: CapabilityAgentContext, Config: map[string]any{"directory": "/home/agent/a", "filename": "AGENTS.md"}},
	} {
		t.Run(entry.Type, func(t *testing.T) {
			first, second := entry, entry
			first.Order, second.Order = orderPointer(100), orderPointer(200)
			inputs := []Contribution{{Reference: "one", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{first}}}, {Reference: "two", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{second}}}}
			_, err := Compose(inputs)
			require.ErrorContains(t, err, "conflicting effective orders")
			require.ErrorContains(t, err, "one")
			require.ErrorContains(t, err, "two")
			second.Order = first.Order
			inputs[1].Descriptor.Capabilities[0] = second
			out, err := Compose(inputs)
			require.NoError(t, err)
			require.Len(t, out.Capabilities, 1)
			require.Equal(t, first.Order, out.Capabilities[0].Order)
		})
	}
	first := Capability{Type: CapabilityDockerd}
	second := first
	second.Order = orderPointer(BuiltinExecutionContracts()[CapabilityDockerd].DefaultOrder)
	out, err := Compose([]Contribution{{Reference: "one", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{first}}}, {Reference: "two", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{second}}}})
	require.NoError(t, err)
	require.Equal(t, second.Order, out.Capabilities[0].Order)
}
