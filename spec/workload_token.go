package spec

import (
	"fmt"
	"regexp"
	"strings"
)

// workloadTokenName bounds a token name: it is a ledger record key and a
// proxy service handle, so it stays short and shell-safe.
var workloadTokenName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// Defaults an inject rule takes when it names only a domain.
const (
	WorkloadTokenDefaultHeader = "Authorization"
	WorkloadTokenDefaultFormat = "Bearer %s"
)

// validateWorkloadTokenNeed decodes and checks one workload-token entry.
// Every field is required: a token without an audience is unverifiable,
// and one with nowhere to go is never presented.
func validateWorkloadTokenNeed(path string, i int, n Capability) (*WorkloadToken, error) {
	var errs ValidationErrors
	var w WorkloadToken
	if err := DecodeCapabilityConfig(n, &w); err != nil {
		errs.add(configErrorf(path, i, err))
		return nil, errs.err()
	}
	for _, at := range nullConfigPaths("config", n.Config, "") {
		errs.add(fieldErrorf(path+"."+at, "capabilities[%d]: workload-token %s is null; declare a value", i, strings.TrimPrefix(at, "config.")))
	}
	switch {
	case w.Name == "":
		errs.add(fieldErrorf(path+".config.name", "capabilities[%d]: workload-token name is required", i))
	case ContainsArgRef(w.Name) || ContainsEnvRef(w.Name):
	case !workloadTokenName.MatchString(w.Name):
		errs.add(fieldErrorf(path+".config.name", "capabilities[%d]: workload-token name %q must match %s", i, w.Name, workloadTokenName))
	}
	switch {
	case w.Audience == "":
		errs.add(fieldErrorf(path+".config.audience", "capabilities[%d] (%s): workload-token audience is required", i, w.Name))
	case ContainsArgRef(w.Audience) || ContainsEnvRef(w.Audience):
	case len(w.Audience) > 256 || strings.ContainsAny(w.Audience, " \t\r\n"):
		errs.add(fieldErrorf(path+".config.audience", "capabilities[%d] (%s): workload-token audience must be at most 256 characters without whitespace", i, w.Name))
	}
	if len(w.Inject) == 0 {
		errs.add(fieldErrorf(path+".config.inject", "capabilities[%d] (%s): workload-token needs at least one inject rule", i, w.Name))
	}
	for j, inj := range w.Inject {
		at := fmt.Sprintf("%s.config.inject[%d]", path, j)
		if inj.Domain == "" {
			errs.add(fieldErrorf(at+".domain", "capabilities[%d] (%s): inject[%d]: domain is required", i, w.Name, j))
		}
		if inj.Scheme != "" || inj.Username != "" {
			errs.add(fieldErrorf(at, "capabilities[%d] (%s): inject[%d]: a workload token is presented as a header; scheme and username do not apply", i, w.Name, j))
		}
		if inj.Format != "" && !ContainsArgRef(inj.Format) && !ContainsEnvRef(inj.Format) && strings.Count(inj.Format, "%s") != 1 {
			errs.add(fieldErrorf(at+".format", "capabilities[%d] (%s): inject[%d]: format must contain exactly one %%s", i, w.Name, j))
		}
	}
	if err := errs.err(); err != nil {
		return nil, err
	}
	return &w, nil
}

// validateWorkloadTokenInjectWithinAllow holds every token's inject
// domains to the runtime allow list, as credential injects are held:
// a token is presented only where the sandbox may already reach.
func validateWorkloadTokenInjectWithinAllow(needs []Capability, invalid map[int]bool) error {
	var errs ValidationErrors
	policy, err := NetworkPolicyV2Of(needs)
	if err != nil {
		return err
	}
	allow := phaseAllow(policy, "runtime")
	for i, n := range needs {
		if n.Type != CapabilityWorkloadToken || invalid[i] {
			continue
		}
		var w WorkloadToken
		if err := DecodeCapabilityConfig(n, &w); err != nil {
			errs.add(err)
			continue
		}
		for j, inj := range w.Inject {
			if !allow[stripPort(inj.Domain)] && !allow["*"] && !allow["**"] {
				errs.add(fieldErrorf(fmt.Sprintf("capabilities[%d].config.inject[%d].domain", i, j),
					"capabilities[%d] (%s): inject domain %q is not in the network policy's runtime allow list", i, w.Name, inj.Domain))
			}
		}
	}
	return errs.err()
}

// InjectHeader returns the header an inject rule presents a token in.
func (w WorkloadToken) InjectHeader(inj Inject) string {
	if inj.Header == "" {
		return WorkloadTokenDefaultHeader
	}
	return inj.Header
}

// InjectFormat returns the format an inject rule renders a token with.
func (w WorkloadToken) InjectFormat(inj Inject) string {
	if inj.Format == "" {
		return WorkloadTokenDefaultFormat
	}
	return inj.Format
}
