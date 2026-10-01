package spec

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func environmentDescriptor(value string) *Descriptor {
	return &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityLifecycle, Config: map[string]any{"files": []any{map[string]any{"path": "${{ kit.env.HOME }}/config", "content": value}}}},
	}}
}

func TestEnvironmentExpansionPreservesStrings(t *testing.T) {
	for _, value := range []string{"", "001", "true", "123", "quotes\"\\newline\n", "$HOME ${HOME} ~/"} {
		d := environmentDescriptor("prefix:${{ kit.env.VALUE }}:${{kit.env.VALUE}}")
		before, err := json.Marshal(d)
		require.NoError(t, err)
		out, err := ExpandEnvironment(d, map[string]string{"HOME": "/home/user", "VALUE": value})
		require.NoError(t, err)
		lc, err := LifecycleOf(out.Capabilities)
		require.NoError(t, err)
		require.Equal(t, "/home/user/config", lc.Files[0].Path)
		require.Equal(t, "prefix:"+value+":"+value, lc.Files[0].Content)
		after, err := json.Marshal(d)
		require.NoError(t, err)
		require.Equal(t, before, after)
		whole, err := ExpandEnvironment(environmentDescriptor("${{ kit.env.VALUE }}"), map[string]string{"HOME": "/h", "VALUE": value})
		require.NoError(t, err)
		lc, err = LifecycleOf(whole.Capabilities)
		require.NoError(t, err)
		require.Equal(t, value, lc.Files[0].Content)
	}
}

func TestEnvironmentExpansionRejectsInvalidInputs(t *testing.T) {
	t.Setenv("HOME", "/host-must-not-be-used")
	for _, tc := range []struct{ name, template, value, message string }{
		{"missing", "${{kit.env.MISSING}}", "", "MISSING"},
		{"malformed", "${{kit.env.123}}", "", "malformed"},
		{"unfinished", "${{kit.env.VALUE", "", "malformed"},
		{"NUL", "${{kit.env.VALUE}}", "secret\x00", "NUL"},
		{"recursive environment", "${{kit.env.VALUE}}", "${{kit.env.HOME}}", "placeholders"},
		{"recursive argument", "${{kit.env.VALUE}}", "${{kit.args.x}}", "placeholders"},
		{"malformed argument", "${{kit.env.VALUE}}", "secret ${{ kit.args.123 }}", "placeholders"},
		{"unfinished argument", "${{kit.env.VALUE}}", "secret ${{ kit.args.NAME", "placeholders"},
		{"argument with newline", "${{kit.env.VALUE}}", "secret ${{\nkit.args.NAME", "placeholders"},
		{"budget", "${{kit.env.VALUE}}", strings.Repeat("secret", SizeErrorBytes), "budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ExpandEnvironment(environmentDescriptor(tc.template), map[string]string{"HOME": "/h", "VALUE": tc.value})
			require.Nil(t, out)
			require.ErrorContains(t, err, tc.message)
			require.NotContains(t, err.Error(), "secret")
		})
	}
	_, err := ExpandEnvironment(environmentDescriptor("literal"), nil)
	require.ErrorContains(t, err, `variable "HOME"`)
}

func TestEnvironmentDeclarationsDeferButSelectionRequiresExpansion(t *testing.T) {
	d := environmentDescriptor("literal")
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	_, err = ValidatePublished(raw, d)
	require.NoError(t, err)
	_, err = ValidateEffective(raw, d)
	require.ErrorContains(t, err, "kit.env")
	_, err = SelectCapabilities(t.Context(), d, func(context.Context, Descriptor, Capability) CapabilityDecision {
		t.Fatal("selector saw unresolved environment")
		return CapabilityDecision{Accepted: true}
	})
	require.ErrorContains(t, err, "environment")
	out, err := ExpandEnvironment(d, map[string]string{"HOME": "/home/user"})
	require.NoError(t, err)
	raw, err = json.Marshal(out)
	require.NoError(t, err)
	_, err = ValidateEffective(raw, out)
	require.NoError(t, err)
}

func TestEnvironmentExpansionScope(t *testing.T) {
	d := environmentDescriptor("${{kit.env.VALUE}}")
	d.Description = "${{kit.env.METADATA}}"
	d.Capabilities[0].Name = "${{kit.env.LABEL}}"
	d.Capabilities = []Capability{{Group: &CapabilityGroup{Name: "${{kit.env.GROUP}}", Capabilities: d.Capabilities}}}
	out, err := ExpandEnvironment(d, map[string]string{"HOME": "/h", "VALUE": "data"})
	require.NoError(t, err)
	require.Equal(t, d.Description, out.Description)
	require.Equal(t, d.Capabilities[0].Group.Name, out.Capabilities[0].Group.Name)
	require.Equal(t, d.Capabilities[0].Group.Capabilities[0].Name, out.Capabilities[0].Group.Capabilities[0].Name)
	out.Capabilities[0].Group.Capabilities[0].Config["${{kit.env.KEY}}"] = "literal"
	_, err = ExpandEnvironment(out, map[string]string{"KEY": "files"})
	require.ErrorContains(t, err, "mapping keys")
}

func TestMergePreservesEnvironmentUntilCreate(t *testing.T) {
	mixin := environmentDescriptor("${{kit.env.VALUE}}")
	mixin.Capabilities = append(mixin.Capabilities, Capability{Type: CapabilityAgentContext, Config: map[string]any{"content": "Home is ${{kit.env.HOME}}"}})
	merged, err := Merge([]Contribution{{Reference: "mixin", Descriptor: mixin}}, MergeOptions{})
	require.NoError(t, err)
	require.Empty(t, merged.ContextSources, "environment templates in inline content must not be baked into a file")
	require.True(t, HasGroups(merged.Descriptor.Capabilities))
	raw, err := json.Marshal(merged.Descriptor)
	require.NoError(t, err)
	_, err = ValidatePublished(raw, merged.Descriptor)
	require.NoError(t, err)
	expanded, err := ExpandEnvironment(merged.Descriptor, map[string]string{"HOME": "/home/user", "VALUE": "data"})
	require.NoError(t, err)
	selected, err := SelectCapabilities(t.Context(), expanded, Supported(KnownCapabilities()...))
	require.NoError(t, err)
	d := *expanded
	d.Capabilities = selected.Capabilities
	composed, err := Compose([]Contribution{{Reference: "mixin", Descriptor: &d}})
	require.NoError(t, err)
	lc, err := LifecycleOf(composed.Capabilities)
	require.NoError(t, err)
	require.Equal(t, "/home/user/config", lc.Files[0].Path)
	require.Equal(t, "data", lc.Files[0].Content)
}

func TestEnvironmentSchemaShapes(t *testing.T) {
	schema := loadJSON(t, "../schema/definitions/kit-arg.schema.json")
	bearing := regexp.MustCompile(at(t, schema, "definitions", "bearing")["pattern"].(string))
	whole := regexp.MustCompile(at(t, schema, "definitions", "whole")["pattern"].(string))
	for _, value := range []string{"${{kit.env.HOME}}", "${{ kit.env.HOME }}/config", "${{kit.args.home}}"} {
		require.True(t, bearing.MatchString(value))
	}
	require.False(t, bearing.MatchString("${{kit.env.123}}"))
	require.False(t, whole.MatchString("${{kit.env.PORT}}"), "environment values never adopt numeric types")
}

func TestEnvironmentExpansionPreservesUnrelatedNumbers(t *testing.T) {
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{{
		Type: "com.example/custom@1", Config: map[string]any{"counter": int64(9007199254740993), "message": "${{kit.env.VALUE}}"},
	}}}
	out, err := ExpandEnvironment(d, map[string]string{"VALUE": "data"})
	require.NoError(t, err)
	raw, err := json.Marshal(out.Capabilities[0].Config)
	require.NoError(t, err)
	require.JSONEq(t, `{"counter":9007199254740993,"message":"data"}`, string(raw))
	require.Contains(t, string(raw), `9007199254740993`)
}

func TestEnvironmentReferencesWithEscapedWhitespace(t *testing.T) {
	d := environmentDescriptor("${{\nkit.env.VALUE}}")
	d.Capabilities[0].Config["files"].([]any)[0].(map[string]any)["path"] = "${{\nkit.env.HOME}}/config"
	d.Capabilities = []Capability{{Group: &CapabilityGroup{Capabilities: d.Capabilities}}}
	require.True(t, HasEnvReferences(d.Capabilities))
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	require.False(t, ContainsEnvRef(string(raw)))
	_, err = ValidatePublished(raw, d)
	require.NoError(t, err)
	_, err = SelectCapabilities(t.Context(), d, func(context.Context, Descriptor, Capability) CapabilityDecision {
		t.Fatal("unexpanded reference reached selector")
		return CapabilityDecision{Accepted: false}
	})
	require.ErrorContains(t, err, "environment")
	_, err = ValidateEffective(raw, d)
	require.ErrorContains(t, err, "kit.env")
	merged, err := Merge([]Contribution{{Reference: "kit", Descriptor: d}}, MergeOptions{})
	require.NoError(t, err)
	require.True(t, HasEnvReferences(merged.Descriptor.Capabilities))
	out, err := ExpandEnvironment(d, map[string]string{"HOME": "/home/user", "VALUE": "data"})
	require.NoError(t, err)
	require.False(t, HasEnvReferences(out.Capabilities))
}

func TestEnvironmentMappingKeysFailAtPublication(t *testing.T) {
	for i, config := range []map[string]any{
		{"${{ kit.env.KEY }}": "literal"},
		{"nested": []map[string]string{{"${{\nkit.env.KEY}}": "literal"}}},
		{"${{kit.env.KEY": "literal"},
		{"${{kit.env.123}}": "literal"},
		{"${{kit.env.KEY}}": "${{kit.env.VALUE}}"},
	} {
		for _, capabilityType := range []string{CapabilityLifecycle, "com.example/custom@1"} {
			for _, grouped := range []bool{false, true} {
				c := Capability{Type: capabilityType, Config: config}
				require.Equal(t, i == 4, capabilityIsParameterized(c), "only the last config has a value reference that can defer typed validation")
				d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{c}}
				if grouped {
					d.Capabilities = []Capability{{Group: &CapabilityGroup{Optional: true, Capabilities: d.Capabilities}}}
				}
				raw, err := json.Marshal(d)
				require.NoError(t, err)
				for _, validate := range []func([]byte, *Descriptor) ([]string, error){ValidatePublished, ValidateDeclarations, ValidateEffective} {
					_, err := validate(raw, d)
					require.ErrorContains(t, err, "environment references are not allowed in mapping keys")
				}
				_, err = SelectCapabilities(t.Context(), d, func(context.Context, Descriptor, Capability) CapabilityDecision {
					t.Fatal("invalid mapping key reached selector")
					return CapabilityDecision{Accepted: false}
				})
				require.ErrorContains(t, err, "mapping keys")
			}
		}
	}
}

func TestContainsKitPlaceholder(t *testing.T) {
	for _, value := range []string{"${{kit.args.NAME}}", "${{ kit.args.123 }}", "${{ kit.args.NAME", "${{\nkit.args.NAME", "${{kit.env.HOME}}", "${{kit.env.123}}", "${{kit.env.HOME"} {
		require.True(t, ContainsKitPlaceholder(value), "%q", value)
	}
	for _, value := range []string{"$HOME", "${HOME}", "~/", "kit.args.NAME", "${{ unrelated.NAME }}", "${{ kit.argsExtra.NAME }}"} {
		require.False(t, ContainsKitPlaceholder(value), "%q", value)
	}
}

func TestCreateArgsCannotIntroduceEnvironmentReferences(t *testing.T) {
	decls := map[string]Arg{"value": {Required: true}}
	for _, field := range []string{"content", "${{kit.args.value}}"} {
		value := "${{kit.args.value}}"
		if field != "content" {
			value = "literal"
		}
		d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Args: decls,
			Capabilities: []Capability{{Type: "com.example/custom@1", Config: map[string]any{field: value}}},
		}
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		_, err = ExpandCreateArgs(raw, decls, map[string]string{"value": "${{kit.env.SECRET}}"})
		require.ErrorContains(t, err, "introduces an environment reference")
	}
}
