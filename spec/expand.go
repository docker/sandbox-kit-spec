package spec

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// argRef matches ${{ kit.args.<name> }}. The ${{ opener is not valid shell,
// so this vocabulary can never collide with $VAR or ${VAR}, which pass
// through untouched for the shell at run time.
var argRef = regexp.MustCompile(`\$\{\{\s*kit\.args\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// wholeArgRef matches a string that is nothing but one arg reference.
// That is the spelling §6 gives a meaning of its own — the value
// adopts the referenced arg's type — and the only one a set can
// re-export, because the set's own declaration then bounds exactly
// what the referenced arg holds. A composite like "v${{ kit.args.x }}"
// is bounded by neither side's declaration.
var wholeArgRef = regexp.MustCompile(`^\$\{\{\s*kit\.args\.[A-Za-z_][A-Za-z0-9_]*\s*\}\}$`)

// IsWholeArgRef reports whether s is exactly one arg reference.
func IsWholeArgRef(s string) bool { return wholeArgRef.MatchString(s) }

// ResolveArgs validates supplied values against the declarations and returns
// the effective value map: supplied values first, defaults for the rest. A
// required arg with no supplied value is an error; a supplied value failing
// its enum or pattern is an error; a supplied name the kit never declared is
// an error, because a silently ignored input is a misspelling in disguise.
func ResolveArgs(decls map[string]Arg, supplied map[string]string) (map[string]string, error) {
	for name := range supplied {
		if _, ok := decls[name]; !ok {
			return nil, fmt.Errorf("kit arg %q is not declared by this kit", name)
		}
	}

	values := make(map[string]string, len(decls))
	for name, decl := range decls {
		v, ok := supplied[name]
		if !ok {
			if decl.Required {
				return nil, fmt.Errorf("kit arg %q is required and no value was supplied", name)
			}
			if decl.Default == nil {
				continue
			}
			v = *decl.Default
		}
		if err := checkArgValue(name, decl, v); err != nil {
			return nil, err
		}
		values[name] = v
	}
	return values, nil
}

func checkArgValue(name string, decl Arg, v string) error {
	if len(decl.Enum) > 0 {
		for _, e := range decl.Enum {
			if v == e {
				return nil
			}
		}
		return fmt.Errorf("kit arg %q: value %q is not one of %v", name, v, decl.Enum)
	}
	if decl.Pattern != "" {
		re, err := regexp.Compile("^(?:" + decl.Pattern + ")$")
		if err != nil {
			return fmt.Errorf("kit arg %q: invalid pattern: %w", name, err)
		}
		if !re.MatchString(v) {
			return fmt.Errorf("kit arg %q: value %q does not match pattern %q", name, v, decl.Pattern)
		}
	}
	return nil
}

// ExpandString substitutes ${{ kit.args.<name> }} references in s from
// values. Referencing a name absent from values is an error rather than an
// empty string, so a typo fails loudly instead of producing a hook that runs
// with a hole in it.
func ExpandString(s string, values map[string]string) (string, error) {
	var expandErr error
	out := argRef.ReplaceAllStringFunc(s, func(m string) string {
		name := argRef.FindStringSubmatch(m)[1]
		v, ok := values[name]
		if !ok {
			if expandErr == nil {
				expandErr = fmt.Errorf("reference to undeclared or unresolved kit arg %q", name)
			}
			return m
		}
		return v
	})
	return out, expandErr
}

// ExpandLifecycle applies arg expansion where the design permits it and
// nowhere else: install and startup commands, and files content. The
// capability list is never expanded — the permission gate judges literal
// policy — and the descriptor itself is never rewritten; the returned
// copy exists only for execution.
func ExpandLifecycle(lc *Lifecycle, values map[string]string) (*Lifecycle, error) {
	if lc == nil {
		return nil, nil
	}
	out := *lc

	if len(lc.Install) > 0 {
		out.Install = make([]InstallHook, len(lc.Install))
		for i, h := range lc.Install {
			expanded, err := expandCommand(h.Command, values)
			if err != nil {
				return nil, fmt.Errorf("install hook %d: %w", i, err)
			}
			h.Command = expanded
			out.Install[i] = h
		}
	}
	if len(lc.Startup) > 0 {
		out.Startup = make([]StartupHook, len(lc.Startup))
		for i, h := range lc.Startup {
			expanded, err := expandCommand(h.Command, values)
			if err != nil {
				return nil, fmt.Errorf("startup hook %d: %w", i, err)
			}
			h.Command = expanded
			out.Startup[i] = h
		}
	}
	if len(lc.Files) > 0 {
		out.Files = make([]File, len(lc.Files))
		for i, f := range lc.Files {
			content, err := ExpandString(f.Content, values)
			if err != nil {
				return nil, fmt.Errorf("files[%d] (%s): %w", i, f.Path, err)
			}
			f.Content = content
			out.Files[i] = f
		}
	}
	return &out, nil
}

func expandCommand(c CommandLine, values map[string]string) (CommandLine, error) {
	out := make(CommandLine, len(c))
	for i, part := range c {
		expanded, err := ExpandString(part, values)
		if err != nil {
			return nil, err
		}
		out[i] = expanded
	}
	return out, nil
}

// ExpandBuildArgs expands build-phase references structurally, preserving
// the authored YAML's comments and ordering. Create-phase references remain.
func ExpandBuildArgs(raw []byte, decls map[string]Arg, values map[string]string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("expand build args: %w", err)
	}
	var expandErr error
	originals := map[*yaml.Node]yaml.Node{}
	var expand func(*yaml.Node, bool, bool)
	expand = func(n *yaml.Node, forceText, volumeConfig bool) {
		if expandErr != nil {
			return
		}
		if original, ok := originals[n.Alias]; n.Kind == yaml.AliasNode && ok {
			// Only detach an alias when its use needs different scalar typing.
			original.Anchor = ""
			original.HeadComment, original.LineComment, original.FootComment = n.HeadComment, n.LineComment, n.FootComment
			expand(&original, forceText, volumeConfig)
			if original.Tag != n.Alias.Tag || original.Value != n.Alias.Value {
				*n = original
			} else {
				n.HeadComment, n.LineComment, n.FootComment = original.HeadComment, original.LineComment, original.FootComment
			}
			return
		}
		n.HeadComment = expandString(n.HeadComment, decls, values, "build", &expandErr)
		n.LineComment = expandString(n.LineComment, decls, values, "build", &expandErr)
		n.FootComment = expandString(n.FootComment, decls, values, "build", &expandErr)
		if n.Kind == yaml.ScalarNode {
			var decoded any
			if err := n.Decode(&decoded); err != nil {
				expandErr = err
				return
			}
			if text, ok := decoded.(string); ok && ContainsArgRef(text) {
				if n.Anchor != "" {
					originals[n] = *n
				}
				var value any
				if forceText {
					value = expandString(text, decls, values, "build", &expandErr)
				} else {
					value = expandScalar(text, decls, values, "build", &expandErr)
				}
				var scalar yaml.Node
				if err := scalar.Encode(value); err != nil {
					expandErr = err
					return
				}
				switch value := value.(type) {
				case string:
					scalar.SetString(value)
					// yaml.v3 otherwise emits an unquoted << as a merge key.
					if value == "<<" {
						n.Style |= yaml.DoubleQuotedStyle
					}
				case float64:
					// Match typedScalar's round-trip spelling, not YAML's exponent form.
					scalar.Value = strconv.FormatFloat(value, 'f', -1, 64)
					if _, err := strconv.ParseUint(scalar.Value, 10, 64); err == nil {
						// YAML resolves unsigned integer spellings before floats.
						scalar.Tag = "!!int"
					}
				}
				n.Tag, n.Value = scalar.Tag, scalar.Value
				if n.Tag != "!!str" {
					n.Style = 0
				}
			}
		}
		var capabilityType string
		if n.Kind == yaml.MappingNode {
			// Decode only the header so yaml.v3 resolves inherited types
			// with its merge precedence before config scalars are typed.
			// Other fields stay in the authored tree, including merges.
			var header struct {
				Type any `yaml:"type"`
			}
			if err := n.Decode(&header); err != nil {
				expandErr = err
				return
			}
			capabilityType, _ = header.Type.(string)
		}
		keys := map[string]bool{}
		for i, child := range n.Content {
			isKey := n.Kind == yaml.MappingNode && i%2 == 0
			field := ""
			if n.Kind == yaml.MappingNode && !isKey {
				field = n.Content[i-1].Value
			}
			text := isKey || (volumeConfig && volumeTextField(field))
			config := field == "config" && capabilityType == CapabilityVolume
			expand(child, text, config)
			if expandErr != nil {
				return
			}
			// Merge directives contribute entries, not a literal << key.
			if isKey && child.Tag != "!!merge" {
				var name string
				if err := child.Decode(&name); err != nil {
					expandErr = err
					return
				}
				if keys[name] {
					expandErr = fmt.Errorf("build-phase expansion collapses two keys onto %q", name)
					return
				}
				keys[name] = true
			}
		}
	}
	expand(&doc, false, false)
	if expandErr != nil {
		return nil, expandErr
	}
	return yaml.Marshal(&doc)
}

// ExpandCreateArgs expands references to create-phase args (those NOT
// declaring buildArg:) through the descriptor, producing the EFFECTIVE
// descriptor: the document enforcement, the lock, and the permission gate
// judge. Expansion walks the decoded document — every string value and
// every map key — and re-serializes as JSON, so a value carrying quotes or
// backslashes survives instead of breaking the document it lands in. A
// string that is nothing but a reference adopts the value's own type, so a
// numeric arg can still land in a typed config field (`container: ${{
// kit.args.port }}`) the way v2's substitution could. Volume size and
// mode remain schema strings even when their spellings look numeric. The published
// descriptor is never rewritten — callers expand a copy — and the
// declared enum/pattern constraints, which ride in the signed
// descriptor, bound what values can materialize. A reference with no
// resolved value is an error: a hole in effective policy must fail
// loudly, not enforce an empty string.
func ExpandCreateArgs(raw []byte, decls map[string]Arg, values map[string]string) ([]byte, error) {
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("expand create args: %w", err)
	}
	var expandErr error
	expanded := expandNode(doc, decls, values, &expandErr, false)
	if expandErr != nil {
		return nil, expandErr
	}
	out, err := json.Marshal(expanded)
	if err != nil {
		return nil, fmt.Errorf("expand create args: %w", err)
	}
	return out, nil
}

// expandNode substitutes arg references inside the decoded document rather
// than in its serialized bytes. Textual substitution put the value inside a
// quoted scalar without escaping it, so a quote ended the string early and
// a backslash sequence was reread as an escape: `C:\new\text` arrived as
// `C:`, newline, `ew`, tab, `ext`. Substituting into decoded strings and
// re-serializing makes any value survive by construction.
func expandNode(n any, decls map[string]Arg, values map[string]string, expandErr *error, volumeConfig bool) any {
	switch v := n.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			// A key names something; it is text whatever the value looks
			// like, so it never adopts a type.
			expanded := expandString(key, decls, values, "create", expandErr)
			if _, taken := out[expanded]; taken {
				// Two keys that expand to one key would silently drop a
				// value, and which one survives is map-iteration order.
				if *expandErr == nil {
					*expandErr = fmt.Errorf("create-phase expansion collapses two keys onto %q", expanded)
				}
				continue
			}
			if text, ok := child.(string); ok && volumeConfig && volumeTextField(expanded) {
				out[expanded] = expandString(text, decls, values, "create", expandErr)
			} else {
				config := expanded == "config" && v["type"] == CapabilityVolume
				out[expanded] = expandNode(child, decls, values, expandErr, config)
			}
		}
		return out
	case []any:
		for i, child := range v {
			v[i] = expandNode(child, decls, values, expandErr, false)
		}
		return v
	case string:
		return expandScalar(v, decls, values, "create", expandErr)
	default:
		return n
	}
}

// Volume sizes and octal modes are schema strings even when their values
// look numeric. Preserve their spelling before typedScalar can round it
// or turn an otherwise valid request into a config type error.
func volumeTextField(field string) bool { return field == "size" || field == "mode" }

// expandScalar expands the references in one string. A string that is
// nothing but a reference adopts the value's own type, so a numeric arg
// reaches a numeric config field (`container: ${{ kit.args.port }}`) — the
// case typed validation is deferred for, and which a quoted "8080" cannot
// satisfy. Anything else is substitution into text.
func expandScalar(s string, decls map[string]Arg, values map[string]string, phase string, expandErr *error) any {
	if m := argRef.FindStringSubmatch(s); m != nil && m[0] == s {
		if v, ok := resolveRef(m[1], decls, values, phase, expandErr); ok {
			if phase == "create" && ContainsEnvRef(v) && *expandErr == nil {
				*expandErr = fmt.Errorf("create-phase argument substitution introduces an environment reference")
			}
			return typedScalar(v)
		}
		return s
	}
	return expandString(s, decls, values, phase, expandErr)
}

// expandString substitutes every reference in one string and keeps it a
// string.
func expandString(s string, decls map[string]Arg, values map[string]string, phase string, expandErr *error) string {
	matches := argRef.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	var out strings.Builder
	var literals [][2]int
	writeLiteral := func(text string) {
		start := out.Len()
		out.WriteString(text)
		literals = append(literals, [2]int{start, out.Len()})
	}
	end := 0
	changed := false
	for _, match := range matches {
		writeLiteral(s[end:match[0]])
		value, ok := resolveRef(s[match[2]:match[3]], decls, values, phase, expandErr)
		if ok {
			changed = true
			out.WriteString(value)
		} else {
			writeLiteral(s[match[0]:match[1]])
		}
		end = match[1]
	}
	writeLiteral(s[end:])
	expanded := out.String()
	if phase == "create" && changed && *expandErr == nil {
		// Each environment token must lie entirely in an unchanged literal
		// span. Checking argument values or token counts misses fragments that
		// choose a variable name or complete an opener across substitutions.
		literalIndex := 0
		for _, opener := range envOpener.FindAllStringIndex(expanded, -1) {
			match := envRef.FindStringIndex(expanded[opener[0]:])
			original := false
			if match != nil && match[0] == 0 {
				for literalIndex < len(literals) && literals[literalIndex][1] < opener[0]+match[1] {
					literalIndex++
				}
				original = literalIndex < len(literals) && literals[literalIndex][0] <= opener[0]
			}
			if !original {
				*expandErr = fmt.Errorf("create-phase argument substitution introduces an environment reference")
				break
			}
		}
	}
	return expanded
}

// resolveRef reports the value one reference expands to, and whether this
// pass owns it at all.
func resolveRef(name string, decls map[string]Arg, values map[string]string, phase string, expandErr *error) (string, bool) {
	if (decls[name].BuildArg != "") != (phase == "build") {
		return "", false
	}
	v, ok := values[name]
	if !ok {
		if *expandErr == nil {
			*expandErr = fmt.Errorf("%s-phase kit arg %q has no resolved value", phase, name)
		}
		return "", false
	}
	return v, true
}

// typedScalar reports the value as the type its literal spelling names,
// and only when that spelling survives a round trip: 8080 is an int and
// 1.5 a float, while 1.0, 007, and 1e5 render back differently and stay
// strings — which is what a version or a zero-padded code wants, and what
// a field receiving one expects.
func typedScalar(v string) any {
	if v == "true" || v == "false" {
		return v == "true"
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && strconv.FormatInt(n, 10) == v {
		return n
	}
	// NaN and ±Inf round-trip through ParseFloat and FormatFloat, but JSON
	// has no spelling for them, so adopting one would fail serialization
	// for a value a string field would have taken happily.
	if f, err := strconv.ParseFloat(v, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) &&
		strconv.FormatFloat(f, 'f', -1, 64) == v {
		return f
	}
	return v
}

// ValidateEffective is the create-time gate on the expanded descriptor:
// ValidateRaw with every capability entry validated strictly (no
// placeholder leniency can apply, because no placeholder may remain).
// The effective descriptor is the enforcement input, so a surviving
// reference would become literal policy.
func ValidateEffective(raw []byte, d *Descriptor) ([]string, error) {
	var errs ValidationErrors
	if !d.declarationsOnly && HasGroups(d.Capabilities) {
		errs.add(fieldErrorf("capabilities", "effective descriptor still contains unselected groups"))
	}
	if refs := ReferencedArgs(raw); len(refs) > 0 {
		errs.add(fieldErrorf("", "effective descriptor still references kit args %v; expansion did not run or a value did not resolve", refs))
	}
	if HasEnvReferences(d.Capabilities) {
		errs.add(fieldErrorf("capabilities", "effective configuration still contains kit.env references; expand the final container environment first"))
	}
	warnings, err := ValidateRaw(raw, d)
	errs.add(err)
	return warnings, errs.err()
}

// ReferencedArgs returns the set of arg names referenced anywhere in the
// raw descriptor, for validation that every reference resolves.
func ReferencedArgs(raw []byte) []string {
	seen := map[string]bool{}
	var names []string
	for _, m := range argRef.FindAllSubmatch(raw, -1) {
		name := string(m[1])
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// ContainsArgRef reports whether s references any kit arg.
func ContainsArgRef(s string) bool {
	return strings.Contains(s, "${{") && argRef.MatchString(s)
}

var kitPlaceholderOpener = regexp.MustCompile(`\$\{\{\s*kit\.(args|env)\b`)

// ContainsKitPlaceholder recognizes Kit template openers even when the name
// or closing braces are malformed. Inserted values must remain literal.
func ContainsKitPlaceholder(s string) bool { return kitPlaceholderOpener.MatchString(s) }
