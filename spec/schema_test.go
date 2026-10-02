package spec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The JSON Schemas under schema/ are hand-maintained for editor
// validation and completion; these tests pin their load-bearing
// constants to this package so the two cannot drift silently. They are
// deliberately not a full validator conformance suite — the Go code is
// the validator of record.
//
// Ownership: each well-known need type's config schema lives in its own
// file under schema/capabilities/ — the document the type's @version names —
// and kit.schema.json composes them by reference.

func loadJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}

func loadSchema(t *testing.T) map[string]any {
	t.Helper()
	return loadJSON(t, "../schema/kit.schema.json")
}

func TestAgentContextDirectorySchema(t *testing.T) {
	schema := loadJSON(t, perTypeSchemaPath(CapabilityAgentContext))
	directory := at(t, schema, "properties", "directory")
	require.Equal(t, "string", directory["type"])
	require.Equal(t, []any{"filename"}, at(t, schema, "dependencies")["directory"])
	assertAcceptsKitArg(t, directory, "bearing")
	pattern := regexp.MustCompile(literalPattern(t, directory))
	for _, value := range []string{"/", "/home/agent/.codex", "/home/agent/.kit-tck/context", "", ".codex", "/home/agent/../agent/.codex", "/home/agent/.codex/", "//home/agent"} {
		require.Equal(t, canonicalAbsPath(value), pattern.MatchString(value), "directory %q", value)
	}
}

func perTypeSchemaPath(typ string) string {
	return filepath.Join("..", "schema", "capabilities", filepath.FromSlash(typ)+".schema.json")
}

func at(t *testing.T, m map[string]any, path ...string) map[string]any {
	t.Helper()
	for _, p := range path {
		next, ok := m[p].(map[string]any)
		require.True(t, ok, "schema path %s missing at %q", strings.Join(path, "."), p)
		m = next
	}
	return m
}

func allCapabilityTypes() []string {
	return []string{
		CapabilityNetworkPolicy, CapabilityNetworkPolicyV2, CapabilityCredential,
		CapabilityVolume, CapabilityPort,
		CapabilityUSBDevice, CapabilityResources, CapabilityPrivileged, CapabilityKitRegistry,
		CapabilityAgentSessions, CapabilityAgentInteractiveSessions, CapabilityLifecycle, CapabilityAgentContext,
		CapabilityAgentSkills, CapabilityAgentSkill, CapabilitySbx, CapabilityLongRunning, CapabilitySSHAgent, CapabilityGitIdentity,
	}
}

func TestSchemaMatchesSpecConstants(t *testing.T) {
	schema := loadSchema(t)
	props := at(t, schema, "properties")
	defs := at(t, schema, "definitions")

	require.Equal(t, SchemaVersion, at(t, props, "schemaVersion")["const"])
	require.ElementsMatch(t, []any{KindWorkload, KindMixin, KindSet}, at(t, props, "kind")["enum"].([]any))

	// A listed kit's digest pin is the one spelling the validator compares.
	require.Equal(t, manifestDigest.String(),
		at(t, defs, "kit", "properties", "digest")["pattern"])
	// The set rules exist twice — as conditionals here and as
	// validateKits in the validator — so the schema's verdicts are
	// pinned to the validator's: a set lists kits, and a kits list
	// excludes the two Dockerfile recipes.
	setRules := schema["allOf"].([]any)
	require.Equal(t, KindSet, at(t, setRules[0].(map[string]any), "if", "properties", "kind")["const"])
	require.Equal(t, []any{any("kits")}, at(t, setRules[0].(map[string]any), "then")["required"])
	excluded := at(t, setRules[1].(map[string]any), "then", "not")["anyOf"].([]any)
	var excludedFields []any
	for _, branch := range excluded {
		excludedFields = append(excludedFields, branch.(map[string]any)["required"].([]any)...)
	}
	require.ElementsMatch(t, []any{any("build"), any("dockerfile")}, excludedFields)

	// The workload-only rule exists twice for the same reason, so the
	// schema's verdict is pinned to the validator's.
	var mixinRule map[string]any
	for _, rule := range setRules {
		r := rule.(map[string]any)
		cond, ok := r["if"].(map[string]any)["properties"].(map[string]any)
		if !ok {
			continue
		}
		if kind, ok := cond["kind"].(map[string]any); ok && kind["const"] == KindMixin {
			mixinRule = r
		}
	}
	require.NotNil(t, mixinRule, "the schema states no mixin rule")
	require.Equal(t, CapabilitySbx,
		at(t, mixinRule, "then", "properties", "capabilities", "items", "properties", "type", "not")["const"])

	// The reference pattern rejects what validateKits rejects: a local
	// path names a kit that may not be published at all.
	kitRef := regexp.MustCompile(at(t, defs, "kit", "properties", "ref")["pattern"].(string))
	for _, ref := range []string{"docker.io/me/sbx-kit-gh:2.72.0", "reg.example.com/a/b@sha256:" + strings.Repeat("a", 64), "sbx-kit-gh:1.0.0"} {
		require.True(t, kitRef.MatchString(ref), "schema must accept reference %q", ref)
	}
	// The canonical spelling of a kit-arg reference contains spaces,
	// and a set's registry namespace is exactly what one is for. A
	// pattern forbidding whitespace outright would have an editor
	// reject the descriptors ValidateRaw deliberately accepts — and
	// both set examples are written this way.
	for _, ref := range []string{
		"${{ kit.args.registry }}/sbx-kit-shell:1.0.1",
		"${{kit.args.registry}}/sbx-kit-shell:1.0.1",
		"reg.example.com/${{ kit.args.name }}:1.0.0",
	} {
		require.True(t, kitRef.MatchString(ref), "schema must accept parameterized reference %q", ref)
		require.True(t, ContainsArgRef(ref), "and the validator must see the same token")
	}
	// Whitespace that is not part of a token stays refused, and so
	// does a local path however it is spelled.
	for _, ref := range []string{
		"reg.io/a b:1",
		"${{ kit.args.registry }} /sbx-kit-shell:1.0.1",
		"./${{ kit.args.name }}",
		"${{ kit.args.registry }}/a b:1",
	} {
		require.False(t, kitRef.MatchString(ref), "schema must reject %q", ref)
	}
	for _, ref := range []string{"./gh", "../gh", "/abs/gh", "reg.io/a b:1", ""} {
		require.False(t, kitRef.MatchString(ref), "schema must reject reference %q", ref)
	}
	// The schema is a coarse filter over a grammar it cannot express;
	// validateKitReference parses the reference for real. These are the
	// shapes the filter itself has to catch, so an editor flags them
	// before a build does.
	refExclusions := at(t, defs, "kit", "properties", "ref")["not"].(map[string]any)["anyOf"].([]any)
	var patterns []string
	for _, branch := range refExclusions {
		patterns = append(patterns, branch.(map[string]any)["pattern"].(string))
	}
	require.ElementsMatch(t, []string{`^git\+`, "://"}, patterns)
	for _, ref := range []string{"https://example.com/kit", "git+https://example.com/kit"} {
		var matched bool
		for _, p := range patterns {
			if regexp.MustCompile(p).MatchString(ref) {
				matched = true
			}
		}
		require.True(t, matched, "schema must exclude %q", ref)
	}

	// The need type pattern is the same expression validateCapabilityEntries compiles.
	needTypePattern := at(t, defs, "capability", "properties", "type")["pattern"].(string)
	require.Equal(t, needType.String(), needTypePattern)
	require.Equal(t, "string", at(t, defs, "capability", "properties", "name")["type"])
	require.NotContains(t, at(t, defs, "capability")["required"], "name")

	// Field-shape regexes in the per-type schemas match the validator's.
	// Patterned fields that an authored descriptor may parameterize with
	// ${{ kit.args.* }} use anyOf: the literal grammar first, then a
	// whole/bearing kit-arg ref — matching ValidateRaw's lenient path.
	skills := loadJSON(t, perTypeSchemaPath(CapabilityAgentSkills))
	require.ElementsMatch(t, []any{SkillsReadOnly, SkillsReadWrite},
		literalEnum(t, at(t, skills, "properties", "mode")))
	assertAcceptsKitArg(t, at(t, skills, "properties", "mode"), "bearing")

	// The canonical-path rule exists twice — as path.Clean in the
	// validator and as a regex in the published schema — so the schema's
	// verdicts are pinned to the validator's over every alias shape.
	skillsPath := regexp.MustCompile(literalPattern(t, at(t, skills, "properties", "path")))
	for _, p := range []string{"/skills", "/home/agent/.claude/skills", "/a/..b", "/a/.hidden"} {
		require.True(t, skillsPath.MatchString(p), "schema must accept canonical path %q", p)
	}
	for _, p := range []string{"/", "relative", "/x/../skills", "/skills/", "//skills", "/skills/.", "/a/.."} {
		require.False(t, skillsPath.MatchString(p), "schema must reject alias %q", p)
	}
	assertAcceptsKitArg(t, at(t, skills, "properties", "path"), "bearing")

	credential := loadJSON(t, perTypeSchemaPath(CapabilityCredential))
	require.Equal(t, handleName.String(), literalPattern(t, at(t, credential, "properties", "service")))
	assertAcceptsKitArg(t, at(t, credential, "properties", "service"), "bearing")

	sshAgent := loadJSON(t, perTypeSchemaPath(CapabilitySSHAgent))
	require.Equal(t, sshSignatureNamespace.String(), literalPattern(t, at(t, sshAgent, "properties", "sign", "items")))
	require.Equal(t, sshDestination.String(), literalPattern(t, at(t, sshAgent, "properties", "authenticate", "items")))
	assertAcceptsKitArg(t, at(t, sshAgent, "properties", "sign", "items"), "bearing")
	assertAcceptsKitArg(t, at(t, sshAgent, "properties", "authenticate", "items"), "bearing")
	assertAcceptsKitArg(t, at(t, sshAgent, "properties", "unrestricted"), "whole")
	// Bounds depend on the boolean's expanded value. An omitted value
	// still takes the default; an arg must not take that branch yet.
	sshRule := sshAgent["allOf"].([]any)[0].(map[string]any)
	require.Equal(t, "boolean", at(t, sshRule, "if", "properties", "unrestricted")["type"])
	require.Equal(t, false, at(t, sshRule, "then", "if", "properties", "unrestricted")["const"])
	// The schema wraps the validator's env-var pattern in an optional
	// group: an empty name is the inject-only shape, which the validator
	// accepts only alongside inject rules (a cross-field rule a regex
	// cannot carry).
	require.Equal(t, "^("+strings.Trim(envVarName.String(), "^$")+")?$",
		literalPattern(t, at(t, credential, "definitions", "apiKey", "properties", "name")))
	assertAcceptsKitArg(t, at(t, credential, "definitions", "apiKey", "properties", "name"), "bearing")
	assertAcceptsKitArg(t, at(t, credential, "definitions", "apiKey", "properties", "proxyManaged"), "whole")
	// The named-key anyOf branch pins the strict pattern, so name: ""
	// without inject fails the schema as it fails the validator.
	namedBranch := at(t, credential, "definitions", "apiKey")["anyOf"].([]any)[0].(map[string]any)
	require.Equal(t, envVarName.String(), literalPattern(t, at(t, namedBranch, "properties", "name")))
	assertAcceptsKitArg(t, at(t, namedBranch, "properties", "name"), "bearing")
	// And the inject-only branch pins name to the empty/omitted form, so
	// an invalid non-empty name cannot slip through it.
	injectBranch := at(t, credential, "definitions", "apiKey")["anyOf"].([]any)[1].(map[string]any)
	require.Equal(t, "^$", literalPattern(t, at(t, injectBranch, "properties", "name")))
	assertAcceptsKitArg(t, at(t, injectBranch, "properties", "name"), "bearing")
	require.Equal(t, at(t, sshAgent, "properties", "phase"), at(t, credential, "properties", "phase"))
	phaseBranches := at(t, credential, "properties", "phase")["anyOf"].([]any)
	phaseScalar := phaseBranches[0].(map[string]any)
	phaseList := phaseBranches[1].(map[string]any)
	require.ElementsMatch(t, []any{"install", "runtime"}, literalEnum(t, phaseScalar))
	assertAcceptsKitArg(t, phaseScalar, "bearing")
	require.Equal(t, "array", phaseList["type"])
	require.Equal(t, float64(1), phaseList["minItems"])
	require.Equal(t, true, phaseList["uniqueItems"])
	require.Equal(t, phaseScalar, at(t, phaseList, "items"))
	// The credential-file encodings the validator accepts, pinned so the
	// schema cannot silently admit (or drop) one.
	credentialFile := at(t, credential, "definitions", "oauth", "properties", "credentialFile")
	require.ElementsMatch(t, []any{"json", "toml"},
		literalEnum(t, at(t, credentialFile, "properties", "format")))
	assertAcceptsKitArg(t, at(t, credentialFile, "properties", "format"), "bearing")
	assertAcceptsKitArg(t, at(t, credential, "definitions", "oauth", "properties", "passthrough"), "whole")
	// Under format: toml, structure values are constrained to the TOML
	// value model, which has no null — pinned so the schema keeps
	// rejecting what the validator rejects.
	require.Equal(t, "toml", at(t, credentialFile, "if", "properties", "format")["const"])
	require.Equal(t, "#/definitions/tomlValue",
		at(t, credentialFile, "then", "properties", "structure", "additionalProperties")["$ref"])
	require.NotContains(t, at(t, credential, "definitions", "tomlValue")["type"].([]any), "null")

	volume := loadJSON(t, perTypeSchemaPath(CapabilityVolume))
	require.Equal(t, octalMode.String(), literalPattern(t, at(t, volume, "properties", "mode")))
	require.Equal(t, sizeBytes.String(), literalPattern(t, at(t, volume, "properties", "size")))
	assertAcceptsKitArg(t, at(t, volume, "properties", "size"), "bearing")
	assertAcceptsKitArg(t, at(t, volume, "properties", "mode"), "bearing")
	assertAcceptsKitArg(t, at(t, volume, "properties", "path"), "bearing")
	assertAcceptsKitArg(t, at(t, volume, "properties", "tmpfs"), "whole")

	resources := loadJSON(t, perTypeSchemaPath(CapabilityResources))
	require.Equal(t, sizeBytes.String(), literalPattern(t, at(t, resources, "properties", "memory")))
	assertAcceptsKitArg(t, at(t, resources, "properties", "memory"), "bearing")
	assertAcceptsKitArg(t, at(t, resources, "properties", "cpu"), "whole")

	port := loadJSON(t, perTypeSchemaPath(CapabilityPort))
	assertAcceptsKitArg(t, at(t, port, "properties", "container"), "whole")
	assertAcceptsKitArg(t, at(t, port, "properties", "transport"), "bearing")

	lifecycle := loadJSON(t, perTypeSchemaPath(CapabilityLifecycle))
	fileMode := at(t, lifecycle, "properties", "files", "items", "properties", "mode")
	require.Equal(t, octalMode.String(), literalPattern(t, fileMode))
	assertAcceptsKitArg(t, fileMode, "bearing")
	assertAcceptsKitArg(t, at(t, lifecycle, "properties", "files", "items", "properties", "overwrite"), "whole")
	assertAcceptsKitArg(t, at(t, lifecycle, "properties", "startup", "items", "properties", "background"), "whole")
	installEnv := at(t, lifecycle, "properties", "install", "items", "properties", "env", "items")
	require.Equal(t, envVarName.String(), literalPattern(t, installEnv))
	assertAcceptsKitArg(t, installEnv, "bearing")
	startupEnv := at(t, lifecycle, "properties", "startup", "items", "properties", "env", "items")
	require.Equal(t, envVarName.String(), literalPattern(t, startupEnv))
	assertAcceptsKitArg(t, startupEnv, "bearing")

	// The shared kit-arg fragment's whole pattern is the same expression
	// expand.go uses for a whole-value reference.
	kitArg := loadJSON(t, "../schema/definitions/kit-arg.schema.json")
	require.Equal(t, wholeArgRef.String(), at(t, kitArg, "definitions", "whole")["pattern"])

	// The method enum is the validator's set plus ANY, so a method the
	// schema accepts is one validateNetworkEntry accepts, and stating
	// paths without methods is rejected by both representations.
	netV2 := loadJSON(t, perTypeSchemaPath(CapabilityNetworkPolicyV2))
	entry := at(t, netV2, "definitions", "entryObject")
	wantMethods := []any{any(MethodAny)}
	for _, m := range HTTPMethods() {
		wantMethods = append(wantMethods, any(m))
	}
	methods := at(t, entry, "properties", "methods")
	require.ElementsMatch(t, wantMethods, literalEnum(t, methods["items"].(map[string]any)))
	assertAcceptsKitArg(t, methods["items"].(map[string]any), "bearing")
	require.Equal(t, []any{any("methods")}, entry["dependencies"].(map[string]any)["paths"].([]any))
	assertAcceptsKitArg(t, at(t, entry, "properties", "paths", "items"), "bearing")

	// The literal-hosts bound is the allow entry's alone: a deny wins
	// outright, so an overlap between two of them decides the same way.
	literal := at(t, netV2, "definitions", "literalHost")["pattern"].(string)
	allowBound := at(t, netV2, "definitions", "allowEntry")["oneOf"].([]any)[1].(map[string]any)["allOf"].([]any)[1].(map[string]any)
	require.Equal(t, "#/definitions/literalHost",
		at(t, allowBound, "then", "properties", "hosts")["items"].(map[string]any)["$ref"],
		"a bounded allow entry holds its hosts to literalHost")

	// isHostPattern is the validator's side of the same rule.
	host := regexp.MustCompile(literal)
	for _, h := range []string{"api.example.com", "api.example.com:443", "localhost"} {
		require.True(t, host.MatchString(h), "schema must accept literal host %q", h)
		require.False(t, isHostPattern(h))
	}
	for _, h := range []string{"*", "**", "*.example.com", "api.*.com"} {
		require.False(t, host.MatchString(h), "schema must reject pattern %q", h)
		require.True(t, isHostPattern(h))
	}
}

// literalOrArgRefBranches returns the two-branch anyOf of a property that
// accepts either a literal grammar or a ${{ kit.args.* }} reference.
func literalOrArgRefBranches(t *testing.T, prop map[string]any) []any {
	t.Helper()
	anyOf, ok := prop["anyOf"].([]any)
	require.True(t, ok, "property must use anyOf (literal | kit-arg)")
	require.Len(t, anyOf, 2, "property must have exactly two anyOf branches")
	return anyOf
}

// literalPattern returns the literal-grammar branch of a property that
// accepts either that grammar or a ${{ kit.args.* }} reference.
func literalPattern(t *testing.T, prop map[string]any) string {
	t.Helper()
	anyOf := literalOrArgRefBranches(t, prop)
	pattern, ok := anyOf[0].(map[string]any)["pattern"].(string)
	require.True(t, ok, "first anyOf branch must be the literal pattern")
	return pattern
}

// literalEnum returns the literal enum branch of a property that accepts
// either that enum or a ${{ kit.args.* }} reference.
func literalEnum(t *testing.T, prop map[string]any) []any {
	t.Helper()
	anyOf := literalOrArgRefBranches(t, prop)
	enum, ok := anyOf[0].(map[string]any)["enum"].([]any)
	require.True(t, ok, "first anyOf branch must be the literal enum")
	return enum
}

// assertAcceptsKitArg pins the second anyOf branch to the shared
// whole/bearing kit-arg definition.
func assertAcceptsKitArg(t *testing.T, prop map[string]any, shape string) {
	t.Helper()
	anyOf := literalOrArgRefBranches(t, prop)
	ref, ok := anyOf[1].(map[string]any)["$ref"].(string)
	require.True(t, ok, "second anyOf branch must $ref a kit-arg shape")
	require.Equal(t, "../../definitions/kit-arg.schema.json#/definitions/"+shape, ref)
}

// TestPerTypeCapabilitySchemas pins the per-type schema files: the @version in
// a need type names an addressable config schema, so every well-known
// type must publish one, own its config shape (kit.schema.json only
// composes them by reference), and state whether it takes config at all.
func TestPerTypeCapabilitySchemas(t *testing.T) {
	schema := loadSchema(t)
	defs := at(t, schema, "definitions")

	// Every well-known type has a condition branch referencing its
	// per-type schema file.
	refByType := map[string]string{}
	conditions, ok := at(t, defs, "capability")["allOf"].([]any)
	require.True(t, ok)
	for _, c := range conditions {
		cond := c.(map[string]any)
		typeConst := at(t, cond, "if", "properties", "type")["const"].(string)
		config := at(t, cond, "then", "properties", "config")
		refByType[typeConst] = config["$ref"].(string)
	}
	require.ElementsMatch(t, allCapabilityTypes(), keys(refByType))

	// The same set validate.go enforces, so a type cannot be config-less
	// in one place and config-bearing in the other.
	configless := configlessCapabilities
	for _, typ := range allCapabilityTypes() {
		require.Equal(t, "capabilities/"+typ+".schema.json", refByType[typ],
			"kit.schema.json must reference %s's own schema file", typ)

		perType := loadJSON(t, perTypeSchemaPath(typ))
		require.Equal(t, typ, perType["title"], "%s: title names the type", typ)

		if configless[typ] {
			require.Equal(t, map[string]any{}, perType["not"],
				"%s: config-less type rejects every config value", typ)
			continue
		}
		require.Equal(t, "object", perType["type"], "%s: config is an object", typ)
		require.Equal(t, false, perType["additionalProperties"],
			"%s: strict like the Go decoder", typ)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestSchemaPatternsCompileAsRE2 keeps every schema regex loadable by
// the yaml-language-server's engine (which, like Go, has no lookarounds).
func TestSchemaPatternsCompileAsRE2(t *testing.T) {
	paths := []string{
		"../schema/kit.schema.json",
		"../schema/definitions/kit-arg.schema.json",
	}
	for _, typ := range allCapabilityTypes() {
		paths = append(paths, perTypeSchemaPath(typ))
	}

	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if k == "pattern" {
					if s, ok := val.(string); ok {
						_, err := regexp.Compile(s)
						require.NoError(t, err, "schema pattern %q must compile", s)
					}
					continue
				}
				walk(val)
			}
		case []any:
			for _, item := range x {
				walk(item)
			}
		}
	}
	for _, p := range paths {
		walk(loadJSON(t, p))
	}
}

func TestGroupSchemaMatchesGrammar(t *testing.T) {
	schema := loadSchema(t)
	defs := at(t, schema, "definitions")
	require.Equal(t, "#/definitions/capabilityItem", at(t, schema, "properties", "capabilities", "items")["$ref"])
	require.Len(t, at(t, defs, "capabilityItem")["oneOf"], 2)
	group := at(t, defs, "capabilityGroup")
	require.Equal(t, false, group["additionalProperties"])
	require.Equal(t, float64(1), at(t, group, "properties", "capabilities")["minItems"])
	require.Equal(t, "#/definitions/capabilityMember", at(t, group, "properties", "capabilities", "items")["$ref"])
	member := at(t, defs, "capabilityMember")["allOf"].([]any)
	require.Equal(t, []any{"optional"}, at(t, member[1].(map[string]any), "not")["required"])
	require.ElementsMatch(t, allCapabilityTypes(), KnownCapabilities())
}
