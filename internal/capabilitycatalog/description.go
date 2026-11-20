package capabilitycatalog

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// Intro is the first prose paragraph following the capability page's H1.
// Its small supported Markdown subset matches the current normative pages:
// prose, inline code, and simple inline links. Unsupported markup fails closed.
func descriptionFromPage(page []byte, id, specURL string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(string(page), "\r\n", "\n"), "\n")
	if len(lines) < 4 || lines[0] != "# `"+id+"`" || lines[1] != "" {
		return "", fmt.Errorf("capability %s: expected H1 and blank line before introduction", id)
	}
	var paragraph []string
	for _, line := range lines[2:] {
		if strings.TrimSpace(line) == "" {
			break
		}
		if blockStart.MatchString(strings.TrimSpace(line)) || thematicBreak.MatchString(strings.TrimSpace(line)) || setextUnderline.MatchString(strings.TrimSpace(line)) {
			return "", fmt.Errorf("capability %s: introduction contains a Markdown block boundary", id)
		}
		paragraph = append(paragraph, strings.TrimSpace(line))
	}
	if len(paragraph) == 0 || len(lines) <= 2+len(paragraph) || strings.TrimSpace(lines[2+len(paragraph)]) != "" {
		return "", fmt.Errorf("capability %s: missing introductory paragraph boundary", id)
	}
	description := strings.Join(paragraph, " ")
	rewritten, err := rewriteDescriptionLinks(description, specURL)
	if err != nil {
		return "", fmt.Errorf("capability %s introduction: %w", id, err)
	}
	if err := validateDescription(rewritten, specURL); err != nil {
		return "", fmt.Errorf("capability %s introduction: %w", id, err)
	}
	return rewritten, nil
}

var inlineLink = regexp.MustCompile(`\[([^\[\]\n]+)\]\(([^()\s]+)\)`)
var blockStart = regexp.MustCompile(`^(?:#{1,6}\s|>\s|[-*+]\s|[0-9]+[.)]\s|\x60\x60\x60|~~~|---(?:\s|$))`)
var thematicBreak = regexp.MustCompile(`^(?:\*\s*){3,}$|^(?:_\s*){3,}$|^(?:-\s*){3,}$`)
var setextUnderline = regexp.MustCompile(`^=+$`)

func rewriteDescriptionLinks(description, specURL string) (string, error) {
	return mapDescriptionProse(description, func(prose string) (string, error) {
		return rewriteProseLinks(prose, specURL)
	})
}

func rewriteProseLinks(description, specURL string) (string, error) {
	base, err := url.Parse(specURL)
	if err != nil {
		return "", err
	}
	var firstErr error
	rewritten := inlineLink.ReplaceAllStringFunc(description, func(match string) string {
		if firstErr != nil {
			return match
		}
		parts := inlineLink.FindStringSubmatch(match)
		target, err := url.Parse(parts[2])
		if err != nil {
			firstErr = err
			return match
		}
		if target.Scheme == "" && target.Host == "" {
			if strings.HasPrefix(parts[2], "//") || strings.HasPrefix(parts[2], "/") {
				firstErr = fmt.Errorf("unsafe relative link %q", parts[2])
				return match
			}
			target = base.ResolveReference(target)
			releaseTree, _, ok := strings.Cut(specURL, "/docs/spec/capabilities/")
			if !ok {
				firstErr = fmt.Errorf("invalid specification URL %q", specURL)
				return match
			}
			prefix := releaseTree + "/"
			if !strings.HasPrefix(target.String(), prefix) {
				firstErr = fmt.Errorf("relative link escapes release tree: %q", parts[2])
				return match
			}
		}
		if err := validateDescriptionURL(target, specURL); err != nil {
			firstErr = err
			return match
		}
		return "[" + parts[1] + "](" + target.String() + ")"
	})
	if firstErr != nil {
		return "", firstErr
	}
	return rewritten, nil
}

func validateDescription(description, specURL string) error {
	if description == "" || strings.TrimSpace(description) != description {
		return fmt.Errorf("empty or padded description")
	}
	if blockStart.MatchString(description) || thematicBreak.MatchString(description) {
		return fmt.Errorf("description must be prose")
	}
	for _, r := range description {
		if unicode.IsControl(r) || r == '<' || r == '>' {
			return fmt.Errorf("description contains control or block markup")
		}
	}
	if strings.Contains(description, "![") {
		return fmt.Errorf("description contains image markup")
	}
	_, err := mapDescriptionProse(description, func(prose string) (string, error) {
		return prose, validateProseLinks(prose, specURL)
	})
	return err
}

func validateProseLinks(description, specURL string) error {
	stripped := inlineLink.ReplaceAllString(description, "")
	if strings.ContainsAny(stripped, "[]") {
		return fmt.Errorf("unsupported Markdown link syntax")
	}
	for _, match := range inlineLink.FindAllStringSubmatch(description, -1) {
		target, err := url.Parse(match[2])
		if err != nil {
			return err
		}
		if err := validateDescriptionURL(target, specURL); err != nil {
			return err
		}
	}
	return nil
}

// Markdown links inside code spans are literal examples, not destinations.
func mapDescriptionProse(description string, transform func(string) (string, error)) (string, error) {
	var result strings.Builder
	for {
		start, end := nextCodeSpan(description)
		if start < 0 {
			prose, err := transform(description)
			if err != nil {
				return "", err
			}
			result.WriteString(prose)
			return result.String(), nil
		}
		prose, err := transform(description[:start])
		if err != nil {
			return "", err
		}
		result.WriteString(prose)
		result.WriteString(description[start:end])
		description = description[end:]
	}
}

func nextCodeSpan(text string) (int, int) {
	for start := 0; start < len(text); {
		if text[start] != '`' {
			start++
			continue
		}
		width := len(text[start:]) - len(strings.TrimLeft(text[start:], "`"))
		escapes := 0
		for at := start - 1; at >= 0 && text[at] == '\\'; at-- {
			escapes++
		}
		if escapes%2 == 0 {
			for at := start + width; at < len(text); {
				next := strings.IndexByte(text[at:], '`')
				if next < 0 {
					break
				}
				at += next
				run := len(text[at:]) - len(strings.TrimLeft(text[at:], "`"))
				if run == width {
					return start, at + run
				}
				at += run
			}
		}
		start += width
	}
	return -1, -1
}

func validateDescriptionURL(target *url.URL, specURL string) error {
	if (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" || target.User != nil {
		return fmt.Errorf("non-HTTP(S) description link %q", target.String())
	}
	if target.Host == "github.com" && strings.HasPrefix(target.Path, "/docker/sandbox-kit-spec/") {
		prefix, _, ok := strings.Cut(specURL, "/docs/spec/capabilities/")
		if !ok {
			return fmt.Errorf("invalid specification URL %q", specURL)
		}
		// Links back to this repository must remain on the same release tree.
		if !strings.HasPrefix(target.String(), prefix+"/") {
			return fmt.Errorf("unpinned specification link %q", target.String())
		}
	}
	return nil
}
