package spec

import (
	"fmt"
	"net/url"
	"strings"
)

// validateMCPRegistrationNeed decodes and checks one mcp-registration
// entry. The runtime connects to the URL from outside the sandbox, so it
// is held to the grammar an authorization server issuer is: an HTTPS
// endpoint with nothing in it that could carry a credential or select a
// different resource per request.
func validateMCPRegistrationNeed(path string, i int, n Capability) (*MCPRegistration, error) {
	var errs ValidationErrors
	var m MCPRegistration
	if err := DecodeCapabilityConfig(n, &m); err != nil {
		errs.add(configErrorf(path, i, err))
		return nil, errs.err()
	}
	for _, at := range nullConfigPaths("config", n.Config, "") {
		errs.add(fieldErrorf(path+"."+at, "capabilities[%d]: mcp-registration %s is null; declare a value", i, strings.TrimPrefix(at, "config.")))
	}
	switch {
	case m.Name == "":
		errs.add(fieldErrorf(path+".config.name", "capabilities[%d]: mcp-registration name is required", i))
	case ContainsArgRef(m.Name) || ContainsEnvRef(m.Name):
	case !workloadTokenName.MatchString(m.Name):
		errs.add(fieldErrorf(path+".config.name", "capabilities[%d]: mcp-registration name %q must match %s", i, m.Name, workloadTokenName))
	}
	switch {
	case m.URL == "":
		errs.add(fieldErrorf(path+".config.url", "capabilities[%d] (%s): mcp-registration url is required", i, m.Name))
	case ContainsArgRef(m.URL) || ContainsEnvRef(m.URL):
	case !isIssuerIdentifier(m.URL):
		errs.add(fieldErrorf(path+".config.url", "capabilities[%d] (%s): mcp-registration url must be an https URL of at most %d characters without userinfo, query or fragment", i, m.Name, crossAppAccessMaxURL))
	}
	switch {
	case m.Access == "" || ContainsArgRef(m.Access) || ContainsEnvRef(m.Access):
	case !workloadTokenName.MatchString(m.Access):
		errs.add(fieldErrorf(path+".config.access", "capabilities[%d] (%s): mcp-registration access %q must match %s", i, m.Name, m.Access, workloadTokenName))
	}
	if err := errs.err(); err != nil {
		return nil, err
	}
	return &m, nil
}

// validateMCPRegistrationReach holds every registered server to what the
// descriptor already lets the sandbox reach. The runtime makes the
// connection, so the network boundary does not see it: without this the
// capability would be a way around the allow list, and a way to send one
// application's access token to another's endpoint.
func validateMCPRegistrationReach(needs []Capability, invalid map[int]bool) error {
	var errs ValidationErrors
	policy, err := NetworkPolicyV2Of(needs)
	if err != nil {
		return err
	}
	allow := phaseAllow(policy, "runtime")
	grants := map[string]*CrossAppAccess{}
	for i, n := range needs {
		if n.Type != CapabilityCrossAppAccess {
			continue
		}
		var x CrossAppAccess
		if err := DecodeCapabilityConfig(n, &x); err != nil || invalid[i] {
			// Present but already refused: the registration is not judged
			// against a grant whose own errors are reported.
			grants[x.Name] = nil
			continue
		}
		grants[x.Name] = &x
	}
	for i, n := range needs {
		if n.Type != CapabilityMCPRegistration || invalid[i] {
			continue
		}
		var m MCPRegistration
		if err := DecodeCapabilityConfig(n, &m); err != nil {
			errs.add(err)
			continue
		}
		u, err := url.Parse(m.URL)
		if err != nil {
			continue
		}
		if !allow[u.Hostname()] && !allow["*"] && !allow["**"] {
			errs.add(fieldErrorf(fmt.Sprintf("capabilities[%d].config.url", i), "capabilities[%d] (%s): url host %q is not in the network policy's runtime allow list", i, m.Name, u.Hostname()))
		}
		if m.Access == "" {
			continue
		}
		at := fmt.Sprintf("capabilities[%d].config.access", i)
		grant, declared := grants[m.Access]
		switch {
		case !declared:
			errs.add(fieldErrorf(at, "capabilities[%d] (%s): access %q names no %s entry declared beside it", i, m.Name, m.Access, CapabilityCrossAppAccess))
		case grant == nil:
		case !grantServesEndpoint(*grant, u):
			errs.add(fieldErrorf(at, "capabilities[%d] (%s): cross-app access %q does not serve %s; the url must be on one of its inject domains that takes the token as an Authorization bearer credential, on the default port, and must be its resource when it states one", i, m.Name, m.Access, m.URL))
		}
	}
	return errs.err()
}

// grantServesEndpoint reports whether a grant's access token belongs on
// requests to endpoint: the grant lists the endpoint's host as an inject
// domain that takes a bearer credential, which is how an MCP server is
// authenticated to, and is bound to the endpoint when it is bound to a
// resource at all.
func grantServesEndpoint(x CrossAppAccess, endpoint *url.URL) bool {
	if port := endpoint.Port(); port != "" && port != "443" {
		return false
	}
	host := strings.ToLower(endpoint.Hostname())
	listed := false
	for _, inj := range x.Inject {
		domain, port, _ := strings.Cut(strings.ToLower(inj.Domain), ":")
		if domain == host && (port == "" || port == "443") &&
			strings.EqualFold(x.InjectHeader(inj), WorkloadTokenDefaultHeader) && x.InjectFormat(inj) == WorkloadTokenDefaultFormat {
			listed = true
			break
		}
	}
	if !listed {
		return false
	}
	if x.Resource == "" {
		return true
	}
	resource, err := url.Parse(x.Resource)
	return err == nil && sameEndpoint(endpoint, resource)
}

// sameEndpoint compares two URLs as the spellings of one HTTPS endpoint:
// scheme and host case-insensitively, the default port stated or not,
// and the path with or without a trailing slash.
func sameEndpoint(a, b *url.URL) bool {
	if a.RawQuery != "" || b.RawQuery != "" || a.User != nil || b.User != nil {
		return false
	}
	origin := func(u *url.URL) string {
		port := u.Port()
		if port == "443" {
			port = ""
		}
		return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + ":" + port
	}
	return strings.EqualFold(a.Scheme, "https") && origin(a) == origin(b) &&
		strings.TrimRight(a.EscapedPath(), "/") == strings.TrimRight(b.EscapedPath(), "/")
}
