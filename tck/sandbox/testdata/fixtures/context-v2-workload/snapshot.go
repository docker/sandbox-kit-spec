// The entrypoint records the targets its startup profiles actually index, so
// a runtime cannot repair missing progressive files after launch and pass.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	absoluteLink = regexp.MustCompile("(?:/|\\./)[^\\s\x60\"'<>()\\[\\]]+")
	markdownLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	codeLink     = regexp.MustCompile("`([^`\\s]+/[^`\\s]+)`")
)

func main() {
	observed := map[string]map[string]string{}
	for i := 2; i+1 < len(os.Args); i += 2 {
		profile, snapshot := os.Args[i], os.Args[i+1]
		text, err := os.ReadFile(snapshot)
		if err != nil {
			continue
		}
		candidates := absoluteLink.FindAllString(string(text), -1)
		for _, pattern := range []*regexp.Regexp{markdownLink, codeLink} {
			for _, link := range pattern.FindAllStringSubmatch(string(text), -1) {
				if !strings.Contains(link[1], "://") {
					candidates = append(candidates, link[1])
				}
			}
		}
		observed[profile] = map[string]string{}
		for _, candidate := range candidates {
			candidate = strings.TrimRight(candidate, ".,;:")
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
