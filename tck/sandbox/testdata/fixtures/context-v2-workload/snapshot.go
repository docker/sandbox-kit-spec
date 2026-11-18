// The entrypoint records the targets its startup profiles actually index, so
// a runtime cannot repair missing progressive files after launch and pass.
package main

import (
	"encoding/json"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type contextLink struct {
	destination string
	attribution string
}

type contextLinkEntry struct {
	text    string
	heading bool
}

type contextLinkSpan struct {
	start, end  int
	label       string
	destination string
}

var (
	contextReference  = regexp.MustCompile(`^[ \t]{0,3}\[([^\]\n]+)\]:[ \t]*(.*)$`)
	contextPlainPath  = regexp.MustCompile("(?:(?:\\.\\.?/)+|/)[^\\s\x60\"'<>()\\[\\]]+")
	contextListItem   = regexp.MustCompile(`^([ \t]*)(?:[-+*]|[0-9]{1,9}[.)])[ \t]+`)
	contextCodePath   = regexp.MustCompile("`([^`\\n]+)`")
	contextKitVersion = regexp.MustCompile(`^[^/\s]+@v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)*$`)
)

// Reference definitions belong to the whole document, while attribution
// belongs to the visible link text and its surrounding index entry.
func contextLinks(text string) []contextLink {
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
	heading := ""
	for _, entry := range contextLinkEntries(lines, definitions) {
		line := entry.text
		spans := contextMarkdownSpans(line, references)
		remaining := []byte(line)
		for _, span := range spans {
			for j := span.start; j < span.end; j++ {
				remaining[j] = ' '
			}
		}
		for _, match := range contextCodePath.FindAllSubmatchIndex(remaining, -1) {
			value := string(remaining[match[2]:match[3]])
			// A versioned Kit identity has dots but is visible attribution.
			if !strings.ContainsAny(value, "/.") || contextKitVersion.MatchString(value) {
				continue
			}
			spans = append(spans, contextLinkSpan{start: match[0], end: match[1], destination: value})
			for j := match[0]; j < match[1]; j++ {
				remaining[j] = ' '
			}
		}
		for _, match := range contextPlainPath.FindAllIndex(remaining, -1) {
			spans = append(spans, contextLinkSpan{start: match[0], end: match[1], destination: strings.TrimRight(string(remaining[match[0]:match[1]]), ".,;:")})
		}
		sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
		var visible strings.Builder
		end := 0
		for _, span := range spans {
			visible.WriteString(line[end:span.start])
			visible.WriteString(span.label)
			end = span.end
		}
		visible.WriteString(line[end:])
		attribution := contextUnescape(visible.String())
		if entry.heading {
			heading = attribution
		}
		for _, span := range spans {
			target, err := url.Parse(contextUnescape(span.destination))
			if err == nil && target.Scheme == "" && target.Host == "" && target.Path != "" {
				links = append(links, contextLink{destination: target.Path, attribution: heading + "\n" + attribution})
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
		if strings.HasPrefix(trimmed, "#") && (len(stack) == 0 || indent < stack[0].indent) {
			stack = nil
			paragraph = -1
			entries = append(entries, contextLinkEntry{text: trimmed, heading: true})
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

func contextMarkdownSpans(line string, references map[string]string) []contextLinkSpan {
	var spans []contextLinkSpan
	pairs := contextBracketPairs(line)
	for start := 0; start < len(line); start++ {
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
			destination = references[contextReferenceLabel(id)]
		}
		if destination != "" {
			spans = append(spans, contextLinkSpan{start: start, end: end, label: label, destination: destination})
			start = end - 1
		}
	}
	return spans
}

func main() {
	observed := map[string]map[string]string{}
	for i := 2; i+1 < len(os.Args); i += 2 {
		profile, snapshot := os.Args[i], os.Args[i+1]
		text, err := os.ReadFile(snapshot)
		if err != nil {
			continue
		}
		observed[profile] = map[string]string{}
		for _, link := range contextLinks(string(text)) {
			candidate := link.destination
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(filepath.Dir(profile), candidate)
			}
			body, err := os.ReadFile(candidate)
			if err == nil {
				observed[profile][candidate] = string(body)
			}
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
