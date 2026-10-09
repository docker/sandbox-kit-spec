package spec

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestExpandBuildArgsPreservesValuesVerbatim(t *testing.T) {
	raw := []byte(`schemaVersion: "3"
kind: mixin
description: "${{ kit.args.text }}"
args:
  text:
    buildArg: TEXT
    required: true
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      files:
        - path: /home/agent/result.txt
          content: "value: ${{ kit.args.text }}"
`)
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidateRaw(raw, d)
	require.NoError(t, err)

	for _, value := range []string{
		`say "hello"`,
		`C:\new\text`,
		"line\nbreak",
		`{"json":"looking"}`,
		`trailing\`,
		"ok\"\nauthor: injected\n#",
		"007",
		"1.0",
		"<<",
	} {
		t.Run(value, func(t *testing.T) {
			values, err := ResolveArgs(d.Args, map[string]string{"text": value})
			require.NoError(t, err)
			out, err := ExpandBuildArgs(raw, d.Args, values)
			require.NoError(t, err)
			published, err := Decode(out)
			require.NoError(t, err)
			_, err = ValidatePublished(out, published)
			require.NoError(t, err)
			require.Equal(t, value, published.Description)
			require.Empty(t, published.Author)
			lc, err := LifecycleOf(published.Capabilities)
			require.NoError(t, err)
			require.Equal(t, "value: "+value, lc.Files[0].Content)
		})
	}
}

func TestExpandBuildArgsPreservesVersionSpelling(t *testing.T) {
	raw := []byte(`schemaVersion: "3"
kind: mixin
version: "${{ kit.args.version }}"
provides: [tool]
args: {version: {buildArg: VERSION}}
`)
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidateRaw(raw, d)
	require.NoError(t, err)
	for _, version := range []string{"20260924.1", "0.00001", "10000000000000000000"} {
		t.Run(version, func(t *testing.T) {
			out, err := ExpandBuildArgs(raw, d.Args, map[string]string{"version": version})
			require.NoError(t, err)
			published, err := Decode(out)
			require.NoError(t, err)
			require.Equal(t, version, published.Version)
			_, err = ValidatePublished(out, published)
			require.NoError(t, err)
		})
	}
}

func TestExpandBuildArgsPreservesYAML(t *testing.T) {
	raw := []byte(`# syntax=docker/sandbox-kit:3
# Build ${{ kit.args.text }}
schemaVersion: "3"
kind: mixin
description: &message "${{ kit.args.text }}" # description
args:
  text:
    buildArg: TEXT
    required: true
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      files:
        - path: /home/agent/result.txt
          content: *message
`)
	d, err := Decode(raw)
	require.NoError(t, err)
	value := `say "hello" in C:\new\text`
	out, err := ExpandBuildArgs(raw, d.Args, map[string]string{"text": value})
	require.NoError(t, err)
	require.Contains(t, string(out), "# syntax=docker/sandbox-kit:3")
	require.Contains(t, string(out), "# Build "+value)
	require.Contains(t, string(out), "# description")
	require.Contains(t, string(out), "&message")
	require.Contains(t, string(out), "*message")

	require.Less(t, strings.Index(string(out), "description:"), strings.Index(string(out), "args:"))
	published, err := Decode(out)
	require.NoError(t, err)
	_, err = ValidatePublished(out, published)
	require.NoError(t, err)
	lc, err := LifecycleOf(published.Capabilities)
	require.NoError(t, err)
	require.Equal(t, value, published.Description)
	require.Equal(t, value, lc.Files[0].Content)
}

func TestExpandBuildArgsLeavesCreateReferences(t *testing.T) {
	raw := []byte(`description: "${{ kit.args.build }} / ${{ kit.args.create }}"
content: "${{ kit.args.create }}"
`)
	decls := map[string]Arg{"build": {BuildArg: "BUILD"}, "create": {}}
	out, err := ExpandBuildArgs(raw, decls, map[string]string{"build": "built", "create": "not yet"})
	require.NoError(t, err)
	require.YAMLEq(t, `description: "built / ${{ kit.args.create }}"
content: "${{ kit.args.create }}"
`, string(out))
}

func TestExpandBuildArgsTypesValuesButNotKeys(t *testing.T) {
	raw := []byte(`"${{ kit.args.port }}":
  container: "${{ kit.args.port }}"
  text: "port ${{ kit.args.port }}"
  enabled: "${{ kit.args.enabled }}"
  cpu: "${{ kit.args.cpu }}"
`)
	decls := map[string]Arg{"port": {BuildArg: "PORT"}, "enabled": {BuildArg: "ENABLED"}, "cpu": {BuildArg: "CPU"}}
	out, err := ExpandBuildArgs(raw, decls, map[string]string{"port": "8080", "enabled": "true", "cpu": "0.00001"})
	require.NoError(t, err)
	require.YAMLEq(t, `"8080": {container: 8080, text: "port 8080", enabled: true, cpu: 0.00001}`, string(out))
}

func TestExpandBuildArgsKeepsMergeLikeKeysLiteral(t *testing.T) {
	for _, key := range []string{
		`"${{ kit.args.key }}"`,
		`${{ kit.args.key }}`,
		`!!str ${{ kit.args.key }}`,
	} {
		t.Run(key, func(t *testing.T) {
			raw := []byte(`schemaVersion: "3"
kind: mixin
args: {key: {buildArg: KEY}}
capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      ` + key + `: {runtime: {allow: ["*"]}}
`)
			d, err := Decode(raw)
			require.NoError(t, err)
			_, err = ValidateRaw(raw, d)
			require.NoError(t, err)
			out, err := ExpandBuildArgs(raw, d.Args, map[string]string{"key": "<<"})
			require.NoError(t, err)
			published, err := Decode(out)
			require.NoError(t, err)
			require.Contains(t, published.Capabilities[0].Config, "<<")
			require.NotContains(t, published.Capabilities[0].Config, "runtime")
			_, err = ValidatePublished(out, published)
			require.ErrorContains(t, err, `unknown field "<<"`)
		})
	}
}

func TestExpandBuildArgsPreservesMergeWithLiteralKey(t *testing.T) {
	for _, key := range []string{"<<", "${{ kit.args.key }}"} {
		for _, entries := range []string{
			`  <<: {inherited: "${{ kit.args.text }}", overridden: default}
  *key: literal
`,
			`  *key: literal
  <<: {inherited: "${{ kit.args.text }}", overridden: default}
`,
		} {
			t.Run(key+entries, func(t *testing.T) {
				// yaml.v3 accepts a literal << beside a merge only through an alias.
				raw := []byte(fmt.Sprintf("key: &key %q\nconfig:\n%s  overridden: explicit\n", key, entries))
				var before map[string]any
				require.NoError(t, yaml.Unmarshal(raw, &before))

				decls := map[string]Arg{"key": {BuildArg: "KEY"}, "text": {BuildArg: "TEXT"}}
				out, err := ExpandBuildArgs(raw, decls, map[string]string{"key": "<<", "text": "built"})
				require.NoError(t, err)
				require.Contains(t, string(out), "*key")

				var published map[string]any
				require.NoError(t, yaml.Unmarshal(out, &published))
				require.Equal(t, map[string]any{"<<": "literal", "inherited": "built", "overridden": "explicit"}, published["config"])
			})
		}
	}
}

func TestExpandBuildArgsRejectsCollapsedKeys(t *testing.T) {
	for _, name := range []string{"profile", "<<"} {
		t.Run(name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf("\"${{ kit.args.name }}\": from-arg\n%q: literal\n", name))
			_, err := ExpandBuildArgs(raw, map[string]Arg{"name": {BuildArg: "NAME"}}, map[string]string{"name": name})
			require.ErrorContains(t, err, fmt.Sprintf("collapses two keys onto %q", name))
		})
	}
}

func TestExpandBuildArgsRejectsMissingValues(t *testing.T) {
	_, err := ExpandBuildArgs([]byte(`description: "${{ kit.args.text }}"`), map[string]Arg{"text": {BuildArg: "TEXT"}}, nil)
	require.ErrorContains(t, err, `build-phase kit arg "text" has no resolved value`)
}

func TestExpandBuildArgsTypesAliasesAtTheirUse(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"key to value", `ports:
  &port "${{ kit.args.port }}": open
container: *port # alias ${{ kit.args.comment }}
`, `ports: {"8080": open}
container: 8080
`},
		{"value to key", `container: &port "${{ kit.args.port }}"
ports:
  *port: open # alias ${{ kit.args.comment }}
`, `container: 8080
ports: {"8080": open}
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decls := map[string]Arg{"port": {BuildArg: "PORT"}, "comment": {BuildArg: "COMMENT"}}
			out, err := ExpandBuildArgs([]byte(tc.raw), decls, map[string]string{"port": "8080", "comment": "${{ kit.args.port }}"})
			require.NoError(t, err)
			require.YAMLEq(t, tc.want, string(out))
			require.NotContains(t, string(out), "*port")
			require.Contains(t, string(out), "# alias ${{ kit.args.port }}", "comments expand only once")
		})
	}
}

func TestExpandBuildArgsPreservesScalarAliases(t *testing.T) {
	for _, raw := range []string{
		`first: &value "${{ kit.args.value }}"
second: *value # alias ${{ kit.args.value }}
`,
		`first: {&value "${{ kit.args.value }}": one}
second: {*value: two} # alias ${{ kit.args.value }}
`,
	} {
		for _, value := range []string{"text", "8080", "true", "0.00001", "<<", "${{ kit.args.value }}"} {
			t.Run(raw+value, func(t *testing.T) {
				decls := map[string]Arg{"value": {BuildArg: "VALUE"}}
				out, err := ExpandBuildArgs([]byte(raw), decls, map[string]string{"value": value})
				require.NoError(t, err)
				require.Contains(t, string(out), "*value")
				require.Contains(t, string(out), "# alias "+value)
			})
		}
	}

	// Crossing key/value positions needs no copy when the type stays text.
	raw := []byte(`first: {&value "${{ kit.args.value }}": one}
second: *value
`)
	for _, decls := range []map[string]Arg{{"value": {BuildArg: "VALUE"}}, {"value": {}}} {
		out, err := ExpandBuildArgs(raw, decls, map[string]string{"value": "text"})
		require.NoError(t, err)
		require.Contains(t, string(out), "*value")
	}
}

func TestExpandBuildArgsAliasesStayWithinSizeBudget(t *testing.T) {
	var raw strings.Builder
	raw.WriteString(`schemaVersion: "3"
kind: mixin
args: {x: {buildArg: X}}
capabilities:
- type: com.docker.sandbox/lifecycle@1
  config:
    files:
    - {path: /a, content: &body "${{ kit.args.x }}"}
`)
	for i := range 5 {
		fmt.Fprintf(&raw, "    - {path: /file%d, content: *body}\n", i)
	}
	d, err := Decode([]byte(raw.String()))
	require.NoError(t, err)
	// Six copies fit in JSON, but exceed the YAML budget without aliases.
	value := strings.Repeat("x", 87320)
	out, err := ExpandBuildArgs([]byte(raw.String()), d.Args, map[string]string{"x": value})
	require.NoError(t, err)
	published, err := Decode(out)
	require.NoError(t, err)
	annotation, err := json.Marshal(published)
	require.NoError(t, err)
	require.LessOrEqual(t, len(annotation), SizeErrorBytes)
	require.Equal(t, 5, strings.Count(string(out), "*body"))
	_, err = ValidatePublished(out, published)
	require.NoError(t, err)
	lc, err := LifecycleOf(published.Capabilities)
	require.NoError(t, err)
	require.Len(t, lc.Files, 6)
	for _, file := range lc.Files {
		require.Equal(t, value, file.Content)
	}
}

func TestExpandBuildArgsRejectsAliasedKeyCollisions(t *testing.T) {
	for _, name := range []string{"profile", "<<"} {
		t.Run(name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`name: &key %q
structure:
  <<: {inherited: yes}
  *key: literal
  "${{ kit.args.name }}": expanded
`, name))
			_, err := ExpandBuildArgs(raw, map[string]Arg{"name": {BuildArg: "NAME"}}, map[string]string{"name": name})
			require.ErrorContains(t, err, fmt.Sprintf("collapses two keys onto %q", name))
		})
	}
}

func TestExpandBuildArgsExpandsTaggedStrings(t *testing.T) {
	raw := []byte(`description: !text "${{ kit.args.text }}"`)
	out, err := ExpandBuildArgs(raw, map[string]Arg{"text": {BuildArg: "TEXT"}}, map[string]string{"text": `say "hello"`})
	require.NoError(t, err)
	require.YAMLEq(t, `description: 'say "hello"'`, string(out))
}

func TestExpandArgsKeepsVolumeTextFields(t *testing.T) {
	for _, phase := range []string{"build", "create"} {
		for _, size := range []string{"1024", "1.5", "18446744073709551617"} {
			t.Run(phase+"/"+size, func(t *testing.T) {
				raw := []byte(`schemaVersion: "3"
kind: mixin
args:
  size: {required: true}
  mode: {required: true}
  ram: {required: true}
capabilities:
  - type: com.docker.sandbox/port@1
    config: {container: &numeric "${{ kit.args.mode }}"}
  - group:
      capabilities:
        - type: com.docker.sandbox/volume@1
          config:
            path: /cache
            size: "${{ kit.args.size }}"
            mode: *numeric
            tmpfs: "${{ kit.args.ram }}"
`)
				d, err := Decode(raw)
				require.NoError(t, err)
				if phase == "build" {
					for name, decl := range d.Args {
						decl.BuildArg = strings.ToUpper(name)
						d.Args[name] = decl
					}
				}
				values := map[string]string{"size": size, "mode": "755", "ram": "true"}
				expand := ExpandCreateArgs
				if phase == "build" {
					expand = ExpandBuildArgs
				}
				out, err := expand(raw, d.Args, values)
				require.NoError(t, err)
				effective, err := Decode(out)
				require.NoError(t, err)
				_, err = ValidateExpandedDeclarations(out, effective)
				require.NoError(t, err)
				config := effective.Capabilities[1].Group.Capabilities[0].Config
				require.Equal(t, size, config["size"], "byte spelling and precision survive expansion")
				require.Equal(t, "755", config["mode"], "octal mode remains text even when shared with a numeric field")
				require.Equal(t, true, config["tmpfs"], "boolean fields still adopt boolean values")
				ports, err := PortsOf(effective.Capabilities[:1])
				require.NoError(t, err)
				require.Equal(t, 755, ports[0].Container, "numeric fields still adopt numeric values")
			})
		}
	}
}
