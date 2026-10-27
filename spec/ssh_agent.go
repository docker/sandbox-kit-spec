package spec

import (
	"regexp"
	"sort"
	"strings"
)

var (
	// sshSignatureNamespace is what an SSHSIG namespace may be here:
	// printable ASCII without spaces. The format allows any string; the
	// narrower spelling keeps a namespace one token in a surface entry
	// and on a consent screen.
	sshSignatureNamespace = regexp.MustCompile(`^[!-~]+$`)

	// sshDestination is "host" or "user@host": a literal lowercase DNS
	// name, because the runtime must hold that server's host keys before
	// it can match a session binding to it, and a wildcard names servers
	// nobody has keys for. The user follows the portable login-name
	// shape.
	sshDestination = regexp.MustCompile(`^([a-z_][a-z0-9_.-]*@)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
)

// validateSSHAgentNeed decodes and checks one ssh-agent entry.
func validateSSHAgentNeed(path string, i int, n Capability) (*SSHAgent, error) {
	if err := validateSSHAgentNulls(path, i, n); err != nil {
		return nil, err
	}
	var a SSHAgent
	if err := DecodeCapabilityConfig(n, &a); err != nil {
		return nil, configErrorf(path, i, err)
	}
	if err := validatePhases(path, i, a.Phase); err != nil {
		return nil, err
	}
	if err := validateSSHAgentPresence(path, i, a); err != nil {
		return nil, err
	}
	if err := validateSSHAgentList(path+".config.sign", i, "sign", a.Sign, sshSignatureNamespace,
		"a namespace is printable ASCII without spaces"); err != nil {
		return nil, err
	}
	if err := validateSSHAgentList(path+".config.authenticate", i, "authenticate", a.Authenticate, sshDestination,
		"a destination is host or user@host, with a literal lowercase DNS name and no wildcard, port, or IP address"); err != nil {
		return nil, err
	}
	for _, d := range a.Authenticate {
		if host := sshDestinationHost(d); !ContainsArgRef(d) && !ContainsEnvRef(d) && isAllDigitsAndDots(host) {
			return nil, fieldErrorf(path+".config.authenticate", "capabilities[%d]: ssh-agent destination %q names an IP address; name the server by its DNS name", i, d)
		}
	}
	return &a, nil
}

// validateSSHAgentNulls checks raw fields before JSON decoding turns null
// lists and booleans into omitted-looking nil values.
func validateSSHAgentNulls(path string, i int, n Capability) error {
	for _, field := range []string{"phase", "unrestricted", "sign", "authenticate"} {
		if v, present := n.Config[field]; present && v == nil {
			return fieldErrorf(path+".config."+field, "capabilities[%d]: ssh-agent %s must not be null", i, field)
		}
	}
	return nil
}

// validateSSHAgentPresence checks fields whose presence matters before arg expansion.
func validateSSHAgentPresence(path string, i int, a SSHAgent) error {
	if a.Bounded() && a.Sign == nil && a.Authenticate == nil {
		return fieldErrorf(path+".config.unrestricted", "capabilities[%d]: unrestricted: false requires sign or authenticate", i)
	}
	if !a.Bounded() && (a.Sign != nil || a.Authenticate != nil) {
		return fieldErrorf(path+".config.unrestricted", "capabilities[%d]: sign and authenticate require unrestricted: false", i)
	}
	if a.Sign != nil && len(a.Sign) == 0 {
		return fieldErrorf(path+".config.sign", "capabilities[%d]: ssh-agent entry states an empty sign list", i)
	}
	if a.Authenticate != nil && len(a.Authenticate) == 0 {
		return fieldErrorf(path+".config.authenticate", "capabilities[%d]: ssh-agent entry states an empty authenticate list", i)
	}
	return nil
}

func validateSSHAgentList(path string, i int, field string, values []string, pattern *regexp.Regexp, rule string) error {
	seen := map[string]bool{}
	for _, v := range values {
		if !ContainsArgRef(v) && !ContainsEnvRef(v) && !pattern.MatchString(v) {
			return fieldErrorf(path, "capabilities[%d]: ssh-agent %s value %q is invalid: %s", i, field, v, rule)
		}
		if seen[v] {
			return fieldErrorf(path, "capabilities[%d]: ssh-agent %s lists %q twice", i, field, v)
		}
		seen[v] = true
	}
	return nil
}

// sshDestinationHost is a destination's server, without its user.
func sshDestinationHost(d string) string {
	if at := strings.LastIndexByte(d, '@'); at >= 0 {
		return d[at+1:]
	}
	return d
}

func isAllDigitsAndDots(s string) bool {
	return strings.Trim(s, "0123456789.") == ""
}

// mergeSSHAgents unions the rules for one phase.
func mergeSSHAgents(a, b SSHAgent) SSHAgent {
	if !a.Bounded() || !b.Bounded() {
		return SSHAgent{Phase: a.Phase}
	}
	bounded := false
	return SSHAgent{
		Phase: a.Phase, Unrestricted: &bounded,
		Sign:         unionSorted(a.Sign, b.Sign),
		Authenticate: unionSorted(a.Authenticate, b.Authenticate),
	}
}

// unionSorted returns the sorted union, keeping nil when both are nil so
// an absent list stays absent rather than becoming an empty one.
func unionSorted(a, b []string) []string {
	if a == nil && b == nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, v := range append(append([]string{}, a...), b...) {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// sshAgentSurface projects each phase onto atomic grants: a whole phase
// when unrestricted, or individual namespace and destination bounds.
func sshAgentSurface(a SSHAgent) []string {
	var out []string
	for _, phase := range a.Phase {
		if !a.Bounded() {
			out = append(out, phase)
			continue
		}
		for _, ns := range a.Sign {
			out = append(out, phase+" sign "+ns)
		}
		for _, d := range a.Authenticate {
			out = append(out, phase+" authenticate "+d)
		}
	}
	return out
}

// sshAgentWidenings reports the candidate's grants the granted surface
// does not cover. An unbounded phase covers everything in that phase,
// and a destination without a user covers every user at that host; a
// plain set difference would report narrowing an unbounded grant to a
// bounded one as a widening.
func sshAgentWidenings(granted, candidate []string) []string {
	unbounded := map[string]bool{}
	have := map[string]bool{}
	for _, g := range granted {
		if !strings.Contains(g, " ") {
			unbounded[g] = true
		}
		have[g] = true
	}
	var out []string
	for _, c := range candidate {
		phase, rest, _ := strings.Cut(c, " ")
		switch {
		case have[c], unbounded[phase]:
			continue
		case strings.HasPrefix(rest, "authenticate "):
			d := strings.TrimPrefix(rest, "authenticate ")
			if host := sshDestinationHost(d); host != d && have[phase+" authenticate "+host] {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}
