package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Clipboard directions, as they appear in config and on the surface.
const (
	ClipboardRead  = "read"
	ClipboardWrite = "write"
)

var clipboardDirections = []string{ClipboardRead, ClipboardWrite}

// clipboardPattern is one filter entry: a lowercase media type without
// parameters, a whole top-level type ("image/*"), or every type ("*/*").
// Lowercase and parameter-free so one filter has one spelling, which is
// what lets merges and surfaces compare them as sets.
var clipboardPattern = regexp.MustCompile(`^(\*/\*|[a-z0-9][a-z0-9!#$&^_.+-]*/(\*|[a-z0-9][a-z0-9!#$&^_.+-]*))$`)

// clipboardAliases are the names X11 and older clients use for types a
// filter states as media types. Without them, a filter admitting
// text/plain would refuse the same text requested as UTF8_STRING, and one
// refusing it would leak it under that name.
var clipboardAliases = map[string]string{
	"utf8_string":   "text/plain",
	"string":        "text/plain",
	"text":          "text/plain",
	"compound_text": "text/plain",
	"image/x-png":   "image/png",
}

// CanonicalMediaType is the media type a clipboard filter judges a request
// by: lowercase, parameters dropped, and aliases resolved, so
// "text/plain;charset=utf-8" and "UTF8_STRING" are both text/plain.
func CanonicalMediaType(name string) string {
	t, _, _ := strings.Cut(name, ";")
	t = strings.ToLower(strings.TrimSpace(t))
	if alias, ok := clipboardAliases[t]; ok {
		return alias
	}
	return t
}

// ClipboardFilter is one direction's grant. All admits every type; when
// All is false, Types lists the admitted patterns and an empty list
// admits none.
type ClipboardFilter struct {
	All   bool
	Types []string
}

// Filter returns the grant for a direction. An omitted field admits every
// type: activating the capability is a normal clipboard, and a filter
// only narrows it.
func (c Clipboard) Filter(direction string) ClipboardFilter {
	field := c.Read
	if direction == ClipboardWrite {
		field = c.Write
	}
	if field == nil {
		return ClipboardFilter{All: true}
	}
	return canonicalClipboardFilter(*field)
}

// Enabled reports whether the direction admits any type at all.
func (f ClipboardFilter) Enabled() bool { return f.All || len(f.Types) > 0 }

// Admits reports whether the filter lets a request for mediaType through,
// judged by its canonical type.
func (f ClipboardFilter) Admits(mediaType string) bool {
	if f.All {
		return true
	}
	t := CanonicalMediaType(mediaType)
	for _, p := range f.Types {
		if clipboardPatternContains(p, t) {
			return true
		}
	}
	return false
}

// clipboardPatternContains reports whether pattern p admits everything q
// does: equal, a wildcard over q's top-level type, or every type.
func clipboardPatternContains(p, q string) bool {
	if p == "*/*" || p == q {
		return true
	}
	top, sub, _ := strings.Cut(p, "/")
	return sub == "*" && q != "*/*" && strings.HasPrefix(q, top+"/")
}

// canonicalClipboardFilter drops entries another entry already admits, so
// equal grants have one spelling. Entries are canonicalized the way
// requests are, so an alias validation would refuse still admits what it
// names wherever a filter is built from unvalidated input; "*/*" collapses
// the filter to All.
func canonicalClipboardFilter(entries []string) ClipboardFilter {
	types := make([]string, len(entries))
	for i, t := range entries {
		types[i] = t
		// A reference is not a media type until expansion, and its arg
		// name is case-sensitive.
		if !ContainsArgRef(t) && !envRef.MatchString(t) {
			types[i] = CanonicalMediaType(t)
		}
	}
	var out []string
	for i, t := range types {
		if t == "*/*" {
			return ClipboardFilter{All: true}
		}
		covered := false
		for j, other := range types {
			if i != j && other != t && clipboardPatternContains(other, t) {
				covered = true
				break
			}
		}
		if !covered && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	if out == nil {
		out = []string{}
	}
	return ClipboardFilter{Types: out}
}

// unionClipboardFilter is the grant two requests together ask for.
func unionClipboardFilter(a, b ClipboardFilter) ClipboardFilter {
	if a.All || b.All {
		return ClipboardFilter{All: true}
	}
	return canonicalClipboardFilter(slices.Concat(a.Types, b.Types))
}

// clipboardConfigShape reads a config as its document would state it, so a
// Go-built config holding []string, or a nil slice that encodes as null,
// is judged exactly as the decoded equivalent is.
func clipboardConfigShape(n Capability) (map[string]any, error) {
	raw, err := json.Marshal(n.Config)
	if err != nil {
		return nil, err
	}
	var shape map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&shape); err != nil {
		return nil, err
	}
	return shape, nil
}

// validateClipboardNeed checks a literal clipboard@1 entry.
func validateClipboardNeed(path string, i int, n Capability) error {
	var errs ValidationErrors
	// A null would decode as the omitted default, which grants every type
	// rather than meaning the mistake it is.
	if n.configSet && n.Config == nil {
		return fieldErrorf(path+".config", "capabilities[%d]: config must be an object", i)
	}
	shape, err := clipboardConfigShape(n)
	if err != nil {
		return configErrorf(path, i, err)
	}
	for _, d := range clipboardDirections {
		if value, stated := shape[d]; stated && value == nil {
			errs.add(fieldErrorf(path+".config."+d, "capabilities[%d]: %s must be a list of media types", i, d))
		}
	}
	if err := errs.err(); err != nil {
		return err
	}
	var c Clipboard
	if err := DecodeCapabilityConfig(n, &c); err != nil {
		return configErrorf(path, i, err)
	}
	for _, d := range clipboardDirections {
		field := c.Read
		if d == ClipboardWrite {
			field = c.Write
		}
		if field == nil {
			continue
		}
		for j, t := range *field {
			errs.add(validateClipboardPattern(fmt.Sprintf("%s.config.%s[%d]", path, d, j), i, t, (*field)[:j]))
		}
	}
	if errs.err() == nil && !c.Filter(ClipboardRead).Enabled() && !c.Filter(ClipboardWrite).Enabled() {
		errs.add(fieldErrorf(path+".config", "capabilities[%d]: read and write both admit nothing; an entry granting nothing is refused", i))
	}
	return errs.err()
}

func validateClipboardPattern(at string, i int, t string, earlier []string) error {
	if !clipboardPattern.MatchString(t) {
		return fieldErrorf(at, "capabilities[%d]: %q is not a lowercase media type, type/*, or */*", i, t)
	}
	// Requests are judged by canonical type, so an alias entry would
	// admit nothing; one name per type keeps filters comparable as sets.
	if canonical := CanonicalMediaType(t); canonical != t {
		return fieldErrorf(at, "capabilities[%d]: %q is an alias; write %q", i, t, canonical)
	}
	if slices.Contains(earlier, t) {
		return fieldErrorf(at, "capabilities[%d]: %q is listed twice", i, t)
	}
	return nil
}

// validateParameterizedClipboard judges what a reference cannot hide in a
// clipboard@1 entry, which skips the typed pass until create. Each stated
// direction is a list, never a reference: an arg holds a scalar, so no
// expansion can produce one. Literal entries are judged now; an entry
// holding a reference waits for its value. An entry whose lists are both
// stated empty grants nothing whatever the args say.
func validateParameterizedClipboard(path string, i int, n Capability) error {
	var errs ValidationErrors
	shape, err := clipboardConfigShape(n)
	if err != nil {
		return configErrorf(path, i, err)
	}
	empty := 0
	for _, d := range clipboardDirections {
		value, stated := shape[d]
		if !stated {
			continue
		}
		list, ok := value.([]any)
		if !ok {
			errs.add(fieldErrorf(path+".config."+d, "capabilities[%d]: %s must be a list of media types", i, d))
			continue
		}
		if len(list) == 0 {
			empty++
		}
		var earlier []string
		for j, item := range list {
			at := fmt.Sprintf("%s.config.%s[%d]", path, d, j)
			s, ok := item.(string)
			if !ok {
				errs.add(fieldErrorf(at, "capabilities[%d]: a media type is a string", i))
				continue
			}
			if ContainsArgRef(s) || envRef.MatchString(s) {
				continue
			}
			errs.add(validateClipboardPattern(at, i, s, earlier))
			earlier = append(earlier, s)
		}
	}
	if errs.err() == nil && empty == len(clipboardDirections) {
		errs.add(fieldErrorf(path+".config", "capabilities[%d]: read and write both admit nothing; an entry granting nothing is refused", i))
	}
	return errs.err()
}

// clipboardAsk accumulates clipboard@1 requests. Filters union per
// direction: a Kit that pastes screenshots and a mixin that copies
// answers compose into a sandbox that does both, which is what each
// asked for. The first contributor's display metadata stays, as for the
// other singletons.
type clipboardAsk struct {
	first    Capability
	read     ClipboardFilter
	write    ClipboardFilter
	optional bool
}

func (m *capabilityMerge) addClipboard(reference string, n Capability) error {
	var c Clipboard
	if err := decodeForMerge(reference, n, &c); err != nil {
		return err
	}
	if m.clipboard == nil {
		m.clipboard = &clipboardAsk{first: n, read: c.Filter(ClipboardRead), write: c.Filter(ClipboardWrite), optional: n.Optional}
		return nil
	}
	ask := m.clipboard
	ask.read = unionClipboardFilter(ask.read, c.Filter(ClipboardRead))
	ask.write = unionClipboardFilter(ask.write, c.Filter(ClipboardWrite))
	if ask.first.Name == "" {
		ask.first.Name = n.Name
	}
	// A direction one contributor requires is required of the whole entry:
	// the entry is selected or refused as one.
	ask.optional = ask.optional && n.Optional
	return nil
}

// merged renders the union canonically: a direction admitting every type
// is omitted, and no config at all is the full clipboard, so equal grants
// compare equal however their contributors spelled them.
func (a *clipboardAsk) merged() Capability {
	out := a.first
	out.Optional = a.optional
	// The first contributor's presence flag says nothing about the union:
	// left set over a nil config, it reads as an explicit null, which
	// validation refuses.
	out.Config, out.configSet = nil, false
	for _, d := range []struct {
		name   string
		filter ClipboardFilter
	}{{ClipboardRead, a.read}, {ClipboardWrite, a.write}} {
		if d.filter.All {
			continue
		}
		if out.Config == nil {
			out.Config, out.configSet = map[string]any{}, true
		}
		types := make([]any, 0, len(d.filter.Types))
		for _, t := range d.filter.Types {
			types = append(types, t)
		}
		out.Config[d.name] = types
	}
	return out
}

// clipboardSurface renders each direction's grant as "<direction>
// <pattern>" entries; a direction admitting nothing contributes none.
func clipboardSurface(c Clipboard) []string {
	var out []string
	for _, d := range clipboardDirections {
		f := c.Filter(d)
		if f.All {
			out = append(out, d+" */*")
			continue
		}
		for _, t := range f.Types {
			out = append(out, d+" "+t)
		}
	}
	return out
}

// clipboardWidenings reports the candidate's entries no granted entry of
// the same direction already admits. Containment, not equality: going
// from "read */*" to "read image/*" narrows, and must not stop for
// approval the way a new entry would.
func clipboardWidenings(granted, candidate []string) []string {
	var out []string
	for _, c := range candidate {
		direction, pattern, _ := strings.Cut(c, " ")
		admitted := false
		for _, g := range granted {
			gd, gp, _ := strings.Cut(g, " ")
			if gd == direction && clipboardPatternContains(gp, pattern) {
				admitted = true
				break
			}
		}
		if !admitted {
			out = append(out, c)
		}
	}
	return out
}
