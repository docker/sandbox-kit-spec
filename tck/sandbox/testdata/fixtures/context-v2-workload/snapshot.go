// The entrypoint records the targets its startup profiles actually index, so
// a runtime cannot repair missing progressive files after launch and pass.
package main

import (
	"encoding/json"
	"html"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

type contextLink struct {
	destination string
	attribution string
}

type contextLinkEntry struct {
	text    string
	heading int
}

type contextLinkSpan struct {
	start, end  int
	label       string
	destination string
	markdown    bool
}

var (
	contextReference    = regexp.MustCompile(`^[ \t]{0,3}\[([^\]\n]+)\]:[ \t]*(.*)$`)
	contextPlainPath    = regexp.MustCompile("(?:(?:\\.\\.?/)+|/)[^\\s\x60\"'<>()\\[\\]]+")
	contextListItem     = regexp.MustCompile(`^([ \t]*)(?:[-+*]|[0-9]{1,9}[.)])[ \t]+`)
	contextCodePath     = regexp.MustCompile("`([^`\\n]+)`")
	contextHeading      = regexp.MustCompile(`^(#{1,6})(?:[ \t]+|$)`)
	contextQualifiedKit = regexp.MustCompile(`^(?:localhost|[a-zA-Z0-9-]+(?:\.[a-zA-Z0-9-]+)+)(?::[0-9]+)?/` +
		`(?:[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*/)*[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*` +
		`(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}(?:@sha256:[a-f0-9]{64})?|@sha256:[a-f0-9]{64})$`)
	contextKitVersion = regexp.MustCompile(`^[^/\s]+@v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)*$`)
)

// Reference definitions belong to the whole document, while attribution
// belongs to the visible link text and its surrounding index entry.
func contextLinks(text string) []contextLink {
	return parseContextLinks(text, true)
}

func parseContextLinks(text string, includeAttribution bool) []contextLink {
	// Reference links can repeat a large destination; decode each spelling once.
	decodedDestinations := map[string]string{}
	lines := strings.Split(text, "\n")
	references := map[string]string{}
	definitions := map[int]bool{}
	for i, line := range lines {
		if definitions[i] {
			continue
		}
		match := contextReference.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		destination := match[2]
		continued := strings.TrimSpace(destination) == "" && i+1 < len(lines)
		if continued {
			destination = strings.TrimSpace(lines[i+1])
		}
		target, end, ok := contextLinkDestination(destination, 0)
		if !ok {
			continue
		}
		last := i
		if continued {
			last++
		}
		title := strings.TrimSpace(destination[end:])
		if title != "" {
			last, ok = contextReferenceTitle(lines, last, title)
			if !ok {
				continue
			}
		} else if last+1 < len(lines) {
			if titleEnd, valid := contextReferenceTitle(lines, last+1, strings.TrimSpace(lines[last+1])); valid {
				last = titleEnd
			}
		}
		label := contextReferenceLabel(match[1])
		if _, exists := references[label]; !exists {
			references[label] = target
		}
		for j := i; j <= last; j++ {
			definitions[j] = true
		}
	}
	var links []contextLink
	var headings [6]string
	for _, entry := range contextLinkEntries(lines, definitions) {
		line := entry.text
		spans := contextMarkdownSpans(line, references, !includeAttribution)
		remaining := []byte(line)
		for _, span := range spans {
			for j := span.start; j < span.end; j++ {
				remaining[j] = ' '
			}
		}
		for _, match := range contextCodePath.FindAllSubmatchIndex(remaining, -1) {
			overlap := false
			for _, span := range spans {
				if match[0] < span.end && span.start < match[1] {
					overlap = true
					break
				}
			}
			if overlap {
				continue
			}
			value := string(remaining[match[2]:match[3]])
			// Keep visible Kit identities, but mask every code span before
			// scanning plain paths so slashes in a qualified name stay visible.
			span := contextLinkSpan{start: match[0], end: match[1], label: value}
			if strings.ContainsAny(value, "/.") && !contextKitVersion.MatchString(value) && !contextQualifiedKit.MatchString(value) {
				span.label, span.destination = "", value
			}
			spans = append(spans, span)
			for j := match[0]; j < match[1]; j++ {
				remaining[j] = ' '
			}
		}
		for _, match := range contextPlainPath.FindAllIndex(remaining, -1) {
			spans = append(spans, contextLinkSpan{start: match[0], end: match[1], destination: strings.TrimRight(string(remaining[match[0]:match[1]]), ".,;:")})
		}
		sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
		attribution := ""
		if includeAttribution {
			var visible strings.Builder
			end := 0
			for _, span := range spans {
				visible.WriteString(line[end:span.start])
				visible.WriteString(span.label)
				end = span.end
			}
			visible.WriteString(line[end:])
			attribution = contextUnescape(visible.String())
			if entry.heading > 0 {
				// A heading closes only its own level and deeper sections.
				clear(headings[entry.heading-1:])
				headings[entry.heading-1] = attribution
			}
			attribution = strings.Join(headings[:], "\n") + "\n" + attribution
		}
		for _, span := range spans {
			destination := span.destination
			if span.markdown {
				decoded, cached := decodedDestinations[destination]
				if !cached {
					target, err := url.Parse(contextUnescape(destination))
					if err == nil && target.Scheme == "" && target.Host == "" {
						decoded = target.Path
					}
					decodedDestinations[destination] = decoded
				}
				destination = decoded
			}
			// File paths are literal; only Markdown destinations are URLs.
			if destination != "" {
				links = append(links, contextLink{destination: destination, attribution: attribution})
			}
		}
	}
	return links
}

// Join wrapped links and direct list continuations before scanning. Nested
// items have their own attribution; dedenting resumes the containing item.
// Blank lines end ordinary paragraphs but can occur inside a list item.
func contextLinkEntries(lines []string, definitions map[int]bool) []contextLinkEntry {
	var entries []contextLinkEntry
	type item struct{ entry, indent int }
	var stack []item
	paragraph := -1
	for i, line := range lines {
		if definitions[i] || strings.TrimSpace(line) == "" {
			paragraph = -1
			continue
		}
		// Measure leading tabs in columns without changing link destinations.
		prefix := len(line) - len(strings.TrimLeft(line, " \t"))
		indent := 0
		for _, c := range line[:prefix] {
			if c == '\t' {
				indent += 4 - indent%4
			} else {
				indent++
			}
		}
		line = strings.Repeat(" ", indent) + line[prefix:]
		trimmed := strings.TrimSpace(line)
		if heading := contextHeading.FindStringSubmatch(trimmed); heading != nil && indent < 4 && (len(stack) == 0 || indent < stack[0].indent) {
			stack = nil
			paragraph = -1
			entries = append(entries, contextLinkEntry{text: trimmed, heading: len(heading[1])})
			continue
		}
		marker := contextListItem.FindStringIndex(line)
		// An unindented soft wrap can continue the current paragraph, but
		// a blank line or a new item ends that lazy continuation.
		if marker == nil && len(stack) > 0 && paragraph >= 0 && indent < stack[0].indent {
			entries[paragraph].text += " " + trimmed
			continue
		}
		for len(stack) > 0 && indent < stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}
		if marker != nil {
			stack = append(stack, item{entry: len(entries), indent: marker[1]})
			paragraph = len(entries)
			entries = append(entries, contextLinkEntry{text: line[marker[1]:]})
			if strings.HasPrefix(line[marker[1]:], "#") {
				paragraph = -1
			}
		} else if len(stack) > 0 {
			entry := stack[len(stack)-1].entry
			entries[entry].text += " " + trimmed
			paragraph = entry
		} else if paragraph >= 0 {
			entries[paragraph].text += " " + trimmed
		} else {
			paragraph = len(entries)
			entries = append(entries, contextLinkEntry{text: trimmed})
		}
	}
	return entries
}

// A reference title may start after the destination or on the next line,
// and continue across nonblank lines. None of it is visible attribution.
func contextReferenceTitle(lines []string, last int, title string) (int, bool) {
	if title == "" || !strings.ContainsRune("\"'(", rune(title[0])) {
		return last, false
	}
	close := title[0]
	if close == '(' {
		close = ')'
	}
	at := 1
	for {
		for at < len(title) {
			if title[at] == '\\' {
				at += 2
				continue
			}
			if title[at] == close {
				return last, strings.TrimSpace(title[at+1:]) == ""
			}
			at++
		}
		last++
		if last == len(lines) || strings.TrimSpace(lines[last]) == "" {
			return last, false
		}
		title, at = lines[last], 0
	}
}

func contextReferenceLabel(label string) string {
	return strings.ToLower(strings.Join(strings.Fields(contextUnescape(label)), " "))
}

func contextUnescape(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == '\\' && i+1 < len(text) && strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", rune(text[i+1])) {
			i++
		}
		out.WriteByte(text[i])
	}
	return html.UnescapeString(out.String())
}

func contextBracketPairs(line string) map[int]int {
	pairs := map[int]int{}
	var stack []int
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\\':
			i++
		case '[':
			stack = append(stack, i)
		case ']':
			if len(stack) > 0 {
				pairs[stack[len(stack)-1]] = i
				stack = stack[:len(stack)-1]
			}
		}
	}
	return pairs
}

func contextLinkDestination(line string, start int) (string, int, bool) {
	start = contextSkipSpace(line, start)
	if start == len(line) {
		return "", start, false
	}
	if line[start] == '<' {
		for i := start + 1; i < len(line); i++ {
			switch line[i] {
			case '\\':
				i++
			case '>':
				return line[start+1 : i], i + 1, true
			case '<':
				return "", i, false
			}
		}
		return "", len(line), false
	}
	depth, end := 0, start
	for end < len(line) {
		switch line[end] {
		case '\\':
			if end+1 < len(line) {
				end += 2
				continue
			}
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return line[start:end], end, end > start
			}
			depth--
		case ' ', '\t', '\r':
			return line[start:end], end, depth == 0 && end > start
		}
		end++
	}
	return line[start:end], end, depth == 0 && end > start
}

func contextSkipSpace(line string, at int) int {
	for at < len(line) && (line[at] == ' ' || line[at] == '\t' || line[at] == '\r') {
		at++
	}
	return at
}

func contextInlineEnd(line string, at int) int {
	at = contextSkipSpace(line, at)
	if at < len(line) && (line[at] == '\'' || line[at] == '"' || line[at] == '(') {
		close := line[at]
		if close == '(' {
			close = ')'
		}
		at++
		for at < len(line) && line[at] != close {
			if line[at] == '\\' {
				at++
			}
			at++
		}
		if at == len(line) {
			return -1
		}
		at = contextSkipSpace(line, at+1)
	}
	if at < len(line) && line[at] == ')' {
		return at + 1
	}
	return -1
}

func contextMarkdownSpans(line string, references map[string]string, consumeReferences bool) []contextLinkSpan {
	var spans []contextLinkSpan
	pairs := contextBracketPairs(line)
	codeEnds := map[int]int{}
	for _, span := range contextCodePath.FindAllStringIndex(line, -1) {
		codeEnds[span[0]] = span[1]
	}
	for start := 0; start < len(line); start++ {
		if end, code := codeEnds[start]; code {
			start = end - 1
			continue
		}
		if line[start] == '\\' {
			start++
			continue
		}
		if line[start] != '[' {
			continue
		}
		labelEnd, closed := pairs[start]
		if !closed {
			continue
		}
		label := line[start+1 : labelEnd]
		end, destination := labelEnd+1, ""
		reference := false
		if end < len(line) && line[end] == '(' {
			if target, next, ok := contextLinkDestination(line, end+1); ok {
				if close := contextInlineEnd(line, next); close >= 0 {
					destination, end = target, close
				}
			}
		} else {
			id := label
			if end < len(line) && line[end] == '[' {
				if close, ok := pairs[end]; ok {
					id = line[end+1 : close]
					if id == "" {
						id = label
					}
					end = close + 1
				}
			}
			key := contextReferenceLabel(id)
			destination, reference = references[key]
			if consumeReferences && reference {
				// Retain the span for masking on repeats, without emitting
				// or processing its destination again for startup evidence.
				references[key] = ""
			}
		}
		if destination != "" || reference {
			spans = append(spans, contextLinkSpan{start: start, end: end, label: label, destination: destination, markdown: true})
			start = end - 1
		}
	}
	return spans
}

// These are observer safeguards, not descriptor limits. A file may hold a
// full 512 KiB descriptor's guidance plus the index and existing user text.
// Three such profiles and their linked bodies fit in 16 MiB even charging
// six bytes per byte for JSON escaping, in addition to the input reads.
const (
	observationFileLimit = 1 << 20
	observationBudget    = 16 << 20
	observationEntryCost = 256 // Allow for map entries as well as string data.
)

type observationAllowance struct{ remaining int }

func (b *observationAllowance) reserve(size int) bool {
	if size > b.remaining {
		return false
	}
	b.remaining -= size
	return true
}

// The open itself must not wait for a FIFO writer. Stat the opened handle,
// not the path: a symlink or replacement must not bypass the regular-file
// check. Symlinks to ordinary guidance files remain usable.
func (b *observationAllowance) read(path string, costPerByte int) ([]byte, bool) {
	limit := min(observationFileLimit, b.remaining/costPerByte)
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, false
	}
	// Size can grow after Stat, and some regular pseudo-files report zero.
	// Read one extra byte to reject overflow rather than recording a prefix
	// as valid startup evidence. Charge failed reads against the budget too.
	body, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(body) > limit {
		b.reserve(min(len(body), b.remaining))
		return nil, false
	}
	b.reserve(costPerByte * len(body))
	return body, true
}

func observeContext(profile, snapshot string, allowance *observationAllowance) (map[string]string, bool) {
	if !allowance.reserve(observationEntryCost + 7*len(profile)) {
		return nil, false
	}
	text, ok := allowance.read(snapshot, 1)
	if !ok {
		return nil, false
	}
	observed := map[string]string{}
	seen := map[string]bool{}
	// Startup evidence needs paths, not copies of each link's heading ancestry.
	for _, link := range parseContextLinks(string(text), false) {
		candidate := link.destination
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(filepath.Dir(profile), candidate)
		}
		if seen[candidate] || !allowance.reserve(observationEntryCost+7*len(candidate)) {
			continue
		}
		seen[candidate] = true
		// One input copy plus up to six bytes of JSON per byte. Charging
		// paths and entries above also bounds maps of tiny or empty files.
		if body, ok := allowance.read(candidate, 7); ok {
			observed[candidate] = string(body)
		}
	}
	return observed, true
}

func main() {
	observed := map[string]map[string]string{}
	allowance := &observationAllowance{remaining: observationBudget}
	for i := 2; i+1 < len(os.Args); i += 2 {
		profile, snapshot := os.Args[i], os.Args[i+1]
		if _, exists := observed[profile]; exists {
			continue
		}
		if bodies, ok := observeContext(profile, snapshot, allowance); ok {
			observed[profile] = bodies
		}
	}
	raw, err := json.Marshal(observed)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(os.Args[1], raw, 0o600); err != nil {
		panic(err)
	}
}
