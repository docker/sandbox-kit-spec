package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// nulPaths returns the paths validation reports NUL at, and holds every
// such report to printing no NUL itself.
func nulPaths(t *testing.T, err error) []string {
	t.Helper()
	errs, _ := err.(ValidationErrors)
	var paths []string
	for _, e := range errs {
		fe, ok := e.(*FieldError)
		if !ok || !strings.Contains(fe.Error(), "NUL (U+0000)") {
			continue
		}
		require.NotContains(t, fe.Error(), "\x00", "a NUL report must not print the value")
		paths = append(paths, fe.Path)
	}
	return paths
}

// TestDescriptorStringsRefuseNUL walks each descriptor through the gates
// a create crosses: authoring validation, create-argument expansion, and
// the effective gate. Each case is valid once its NUL is removed, so the
// NUL is what is refused, at the path that holds it.
func TestDescriptorStringsRefuseNUL(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string // YAML after schemaVersion and kind; \0 is a YAML escape
		kind      string // kind, default mixin
		dir       string // value supplied for the dir create argument
		refused   bool   // resolving the create argument refuses its NUL
		authoring string // path authoring validation reports NUL at
		effective string // path the effective gate reports NUL at
	}{
		{name: "clean", body: "description: plain\n"},
		{name: "top-level field", body: "description: \"a\\0b\"\n", authoring: "description"},
		{name: "skills path", body: `capabilities:
  - type: com.docker.sandbox/agent-skills@1
    config: {path: "/skills\0"}
`, authoring: "capabilities[0].config.path"},
		{name: "lifecycle file path", body: `capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config: {files: [{path: "/home/agent/settings.json\0", content: x}]}
`, authoring: "capabilities[0].config.files[0].path"},
		{name: "skill source path", body: `capabilities:
  - type: com.docker.sandbox/agent-skill@1
    config: {path: "/usr/share/skills/review\0", name: review}
`, authoring: "capabilities[0].config.path"},
		{name: "ACP command", body: `capabilities:
  - type: com.docker.sandbox/acp@1
    config: {agent: claude, command: [adapter, "--x\0"]}
`, authoring: "capabilities[0].config.command[1]"},
		{name: "ACP env value", body: `capabilities:
  - type: com.docker.sandbox/acp@1
    config: {agent: claude, command: [adapter], env: {MODE: "a\0"}}
`, authoring: "capabilities[0].config.env.MODE"},
		{name: "config key", body: `capabilities:
  - type: com.docker.sandbox/acp@1
    config: {agent: claude, command: [adapter], env: {"MO\0DE": a}}
`, authoring: "capabilities[0].config.env"},
		{name: "host mount path", body: `capabilities:
  - type: com.docker.sandbox/host-mount@1
    config: {path: "/cache\0"}
`, authoring: "capabilities[0].config.path"},
		{name: "unknown type config", body: `capabilities:
  - type: com.example/thing@1
    optional: true
    config: {nested: [{value: "x\0"}]}
`, authoring: "capabilities[0].config.nested[0].value"},
		{name: "group member", body: `capabilities:
  - group:
      name: cache
      capabilities: [{type: com.docker.sandbox/volume@1, config: {path: "/data\0"}}]
`, authoring: "capabilities[0].group.capabilities[0].config.path"},
		{name: "arg default", body: "args:\n  mode: {default: \"a\\0\"}\n", authoring: "args.mode.default"},
		{name: "arg name", body: "args:\n  \"mo\\0de\": {default: a}\n", authoring: "args"},
		{name: "set kit argument", kind: KindSet, body: "kits:\n  - ref: docker.io/example/kit:1\n    args: {mode: \"a\\0\"}\n", authoring: "kits[0].args.mode"},
		{name: "literal NUL beside an argument", body: `capabilities:
  - type: com.docker.sandbox/volume@1
    config: {path: "/data/${{ kit.args.dir }}\0"}
`, authoring: "capabilities[0].config.path"},
		{name: "argument", body: `capabilities:
  - type: com.docker.sandbox/volume@1
    config: {path: "/data/${{ kit.args.dir }}"}
`},
		{name: "NUL from a create argument", body: `capabilities:
  - type: com.docker.sandbox/volume@1
    config: {path: "/data/${{ kit.args.dir }}"}
`, dir: "cache\x00", refused: true, effective: "capabilities[0].config.path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind := tc.kind
			if kind == "" {
				kind = KindMixin
			}
			header := "schemaVersion: \"3\"\nkind: " + kind + "\n"
			if !strings.Contains(tc.body, "args:") {
				header += "args:\n  dir: {default: cache}\n"
			}
			validate := func(raw string) error {
				d, err := Decode([]byte(raw))
				require.NoError(t, err)
				_, err = ValidateDeclarations([]byte(raw), d)
				return err
			}

			raw := header + tc.body
			if tc.authoring != "" {
				require.Contains(t, nulPaths(t, validate(raw)), tc.authoring)
				require.NoError(t, validate(strings.ReplaceAll(raw, `\0`, "")), "the case must be valid without its NUL")
				return
			}
			require.NoError(t, validate(raw))

			d, err := Decode([]byte(raw))
			require.NoError(t, err)
			supplied := map[string]string{}
			if tc.dir != "" {
				supplied["dir"] = tc.dir
			}
			values, err := KitArgValues(d.Args, supplied)
			if tc.refused {
				require.ErrorContains(t, err, `kit arg "dir": value contains NUL (U+0000)`)
				require.NotContains(t, err.Error(), "\x00", "a NUL report must not print the value")
				// The effective gate still refuses it for a caller that
				// expands the value without resolving it.
				values = supplied
			} else {
				require.NoError(t, err)
			}
			expanded, err := ExpandCreateArgs([]byte(raw), d.Args, values)
			require.NoError(t, err)
			effective, err := Decode(expanded)
			require.NoError(t, err)
			_, err = ValidateEffective(expanded, effective)
			if tc.effective != "" {
				require.Contains(t, nulPaths(t, err), tc.effective)
				return
			}
			require.NoError(t, err)
		})
	}
}

// A value supplied at create is held to the rule every descriptor string
// is, whether a reference carries it into the descriptor or an env export
// carries it into the container environment as it is, and the refusal
// does not print it.
func TestCreateArgValuesRefuseNUL(t *testing.T) {
	text := "a\x00b"
	for _, tc := range []struct {
		name     string
		decl     Arg
		supplied map[string]string
	}{
		{"plain", Arg{}, map[string]string{"value": text}},
		{"environment export", Arg{Env: "VALUE"}, map[string]string{"value": text}},
		{"enum", Arg{Enum: []string{"a", text}}, map[string]string{"value": text}},
		{"pattern", Arg{Pattern: ".*"}, map[string]string{"value": text}},
		{"default", Arg{Default: &text}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decls := map[string]Arg{"value": tc.decl}
			_, err := ResolveArgs(decls, tc.supplied)
			require.EqualError(t, err, `kit arg "value": value contains NUL (U+0000), which no descriptor string may hold`)
			values, err := ResolveArgs(decls, map[string]string{"value": "a"})
			require.NoError(t, err)
			require.Equal(t, "a", values["value"])
		})
	}
}

// TestEveryExampleStringRefusesNUL puts a NUL into each string of every
// example descriptor in turn, values and keys, and requires validation to
// refuse it at that string's path. The positions come from the YAML
// rather than the Go types, so a field the validator's walk missed would
// show up here as an accepted descriptor.
func TestEveryExampleStringRefusesNUL(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "examples", "*", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, matches)
	for _, file := range matches {
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			require.NoError(t, err)
			var root yaml.Node
			require.NoError(t, yaml.Unmarshal(raw, &root))
			for _, s := range yamlStrings(root.Content[0], "") {
				original := s.node.Value
				s.node.Value += "\x00"
				mutated, err := yaml.Marshal(&root)
				s.node.Value = original
				require.NoError(t, err)

				d, err := Decode(mutated)
				if err != nil {
					// Strict decoding refuses a renamed field and a
					// changed schemaVersion before validation runs.
					require.True(t, s.key || s.path == "schemaVersion", "%s: decode refused %s: %v", file, s.path, err)
					continue
				}
				_, err = ValidateDeclarations(mutated, d)
				require.Contains(t, nulPaths(t, err), s.path, "%s: NUL at %s (key %t)", file, s.path, s.key)
			}
		})
	}
}

type yamlString struct {
	node *yaml.Node
	path string // where validation reports it: a key at its map
	key  bool
}

// yamlStrings lists every string scalar under n, in the dotted paths
// Positions uses.
func yamlStrings(n *yaml.Node, path string) []yamlString {
	var out []yamlString
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			out = append(out, yamlString{node: key, path: path, key: true})
			out = append(out, yamlStrings(value, joinFieldPath(path, key.Value))...)
		}
	case yaml.SequenceNode:
		for i, item := range n.Content {
			out = append(out, yamlStrings(item, fmt.Sprintf("%s[%d]", path, i))...)
		}
	case yaml.ScalarNode:
		if n.ShortTag() == "!!str" {
			out = append(out, yamlString{node: n, path: path})
		}
	}
	return out
}

// TestNULRuleIsContainsNUL compares the validator and the schema's one
// spelling of the rule on every short string over an alphabet with NUL,
// as a value at the top level and inside an unknown type's config, and
// as a config key.
func TestNULRuleIsContainsNUL(t *testing.T) {
	noNul := at(t, loadSchema(t), "definitions", "noNul")
	value := regexp.MustCompile(at(t, noNul, "not")["pattern"].(string))
	key := regexp.MustCompile(at(t, noNul, "propertyNames", "not")["pattern"].(string))
	for _, s := range shortStrings() {
		has := strings.ContainsRune(s, 0)
		require.Equal(t, has, value.MatchString(s), "schema value %q", s)
		require.Equal(t, has, key.MatchString(s), "schema key %q", s)
		d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Description: s, Capabilities: []Capability{
			{Type: "com.example/thing@1", Optional: true, Config: map[string]any{"value": s, "k" + s: "v"}},
		}}
		_, err := Validate(d)
		paths := nulPaths(t, err)
		require.Equal(t, has, slices.Contains(paths, "description"), "description %q", s)
		require.Equal(t, has, slices.Contains(paths, "capabilities[0].config.value"), "config value %q", s)
		require.Equal(t, has, slices.Contains(paths, "capabilities[0].config"), "config key %q", s)
	}
}

// shortStrings returns every string of up to five characters over an
// alphabet with path characters, a newline, a non-ASCII letter, and NUL,
// so the rule is compared on all of them rather than on hand-picked
// samples.
func shortStrings() []string {
	all, level := []string{""}, []string{""}
	for range 5 {
		var next []string
		for _, prefix := range level {
			for _, r := range "/.a\né\x00" {
				next = append(next, prefix+string(r))
			}
		}
		all, level = append(all, next...), next
	}
	return all
}

// TestNoNULSchema pins the root rule's shape: one recursive definition
// the root applies, reaching every value, key, and array item.
func TestNoNULSchema(t *testing.T) {
	schema := loadSchema(t)
	require.Contains(t, schema["allOf"], map[string]any{"$ref": "#/definitions/noNul"})
	noNul := at(t, schema, "definitions", "noNul")
	delete(noNul, "description")
	require.Equal(t, map[string]any{
		"not":                  map[string]any{"type": "string", "pattern": "\x00"},
		"propertyNames":        map[string]any{"not": map[string]any{"pattern": "\x00"}},
		"additionalProperties": map[string]any{"$ref": "#/definitions/noNul"},
		"items":                map[string]any{"$ref": "#/definitions/noNul"},
	}, noNul)
}

// TestSchemaSpellsNULOnce holds the schema to the validator's layout: the
// root rule is the only place NUL is spelled, so no field carries a copy
// that could drift from it.
func TestSchemaSpellsNULOnce(t *testing.T) {
	var found []string
	var walk func(file, path string, v any)
	walk = func(file, path string, v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, child := range v {
				walk(file, joinFieldPath(path, k), child)
			}
		case []any:
			for i, child := range v {
				walk(file, fmt.Sprintf("%s[%d]", path, i), child)
			}
		case string:
			if strings.ContainsRune(v, 0) || strings.Contains(v, `\x00`) || strings.Contains(v, `\u0000`) {
				found = append(found, file+":"+path)
			}
		}
	}
	root := filepath.Join("..", "schema")
	require.NoError(t, filepath.WalkDir(root, func(file string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(file) != ".json" {
			return err
		}
		rel, err := filepath.Rel(root, file)
		require.NoError(t, err)
		walk(filepath.ToSlash(rel), "", loadJSON(t, file))
		return nil
	}))
	require.ElementsMatch(t, []string{
		"kit.schema.json:definitions.noNul.not.pattern",
		"kit.schema.json:definitions.noNul.propertyNames.not.pattern",
	}, found)
}

// TestNULWalkTerminatesOnSharedValues covers configs only Go code can
// build: a value that contains itself is refused rather than followed
// forever, and a value reached twice without a cycle is judged at both
// paths.
func TestNULWalkTerminatesOnSharedValues(t *testing.T) {
	self := map[string]any{}
	self["self"] = self
	list := []any{nil}
	list[0] = list
	shared := map[string]any{"v": "x\x00"}
	prefix := []any{"a\x00", nil}
	prefix[1] = prefix[:1]
	for _, tc := range []struct {
		name   string
		config map[string]any
		cyclic []string // paths reported as containing themselves
		nul    []string // paths reported as holding NUL
	}{
		{name: "map holding itself", config: map[string]any{"m": self}, cyclic: []string{"capabilities[0].config.m.self"}},
		{name: "slice holding itself", config: map[string]any{"list": list}, cyclic: []string{"capabilities[0].config.list[0]"}},
		{name: "one map at two keys", config: map[string]any{"a": shared, "b": shared}, nul: []string{"capabilities[0].config.a.v", "capabilities[0].config.b.v"}},
		{name: "slice holding a prefix of itself", config: map[string]any{"list": prefix}, nul: []string{"capabilities[0].config.list[0]", "capabilities[0].config.list[1][0]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNoNUL(&Descriptor{Capabilities: []Capability{{Type: "com.example/thing@1", Config: tc.config}}})
			errs, _ := err.(ValidationErrors)
			var cyclic []string
			for _, e := range errs {
				if fe, ok := e.(*FieldError); ok && strings.Contains(fe.Error(), "contains itself") {
					cyclic = append(cyclic, fe.Path)
				}
			}
			require.ElementsMatch(t, tc.cyclic, cyclic)
			require.ElementsMatch(t, tc.nul, nulPaths(t, err))
		})
	}
}

func joinFieldPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}
