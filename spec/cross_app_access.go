package spec

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// crossAppAccessScope is one OAuth scope token (RFC 6749 section 3.3),
// bounded so a list of them stays a readable ledger field.
var crossAppAccessScope = regexp.MustCompile(`^[\x21\x23-\x5B\x5D-\x7E]{1,128}$`)

// crossAppAccessMaxURL bounds the issuer and resource, which the ledger
// records with every exchange.
const crossAppAccessMaxURL = 256

// validateCrossAppAccessNeed decodes and checks one cross-app-access
// entry. The issuer is where the person's delegated access is redeemed,
// so it must be an HTTPS issuer identifier the runtime can verify
// metadata against rather than any string a token endpoint would accept.
func validateCrossAppAccessNeed(path string, i int, n Capability) (*CrossAppAccess, error) {
	var errs ValidationErrors
	var x CrossAppAccess
	if err := DecodeCapabilityConfig(n, &x); err != nil {
		errs.add(configErrorf(path, i, err))
		return nil, errs.err()
	}
	for _, at := range nullConfigPaths("config", n.Config, "") {
		errs.add(fieldErrorf(path+"."+at, "capabilities[%d]: cross-app-access %s is null; declare a value", i, strings.TrimPrefix(at, "config.")))
	}
	switch {
	case x.Name == "":
		errs.add(fieldErrorf(path+".config.name", "capabilities[%d]: cross-app-access name is required", i))
	case ContainsArgRef(x.Name) || ContainsEnvRef(x.Name):
	case !workloadTokenName.MatchString(x.Name):
		errs.add(fieldErrorf(path+".config.name", "capabilities[%d]: cross-app-access name %q must match %s", i, x.Name, workloadTokenName))
	}
	switch {
	case x.Issuer == "":
		errs.add(fieldErrorf(path+".config.issuer", "capabilities[%d] (%s): cross-app-access issuer is required", i, x.Name))
	case ContainsArgRef(x.Issuer) || ContainsEnvRef(x.Issuer):
	case !isIssuerIdentifier(x.Issuer):
		errs.add(fieldErrorf(path+".config.issuer", "capabilities[%d] (%s): cross-app-access issuer must be an https URL of at most %d characters without userinfo, query or fragment", i, x.Name, crossAppAccessMaxURL))
	}
	switch {
	case x.Resource == "" || ContainsArgRef(x.Resource) || ContainsEnvRef(x.Resource):
	case !isResourceIndicator(x.Resource):
		errs.add(fieldErrorf(path+".config.resource", "capabilities[%d] (%s): cross-app-access resource must be an absolute URI of at most %d characters without a fragment", i, x.Name, crossAppAccessMaxURL))
	}
	seen := map[string]bool{}
	for j, scope := range x.Scopes {
		at := fmt.Sprintf("%s.config.scopes[%d]", path, j)
		switch {
		case ContainsArgRef(scope) || ContainsEnvRef(scope):
		case !crossAppAccessScope.MatchString(scope):
			errs.add(fieldErrorf(at, "capabilities[%d] (%s): scopes[%d] must be one scope token of at most 128 printable characters without spaces, quotes or backslashes", i, x.Name, j))
		case seen[scope]:
			errs.add(fieldErrorf(at, "capabilities[%d] (%s): scope %q is listed twice", i, x.Name, scope))
		}
		seen[scope] = true
	}
	if len(x.Inject) == 0 {
		errs.add(fieldErrorf(path+".config.inject", "capabilities[%d] (%s): cross-app-access needs at least one inject rule", i, x.Name))
	}
	for j, inj := range x.Inject {
		at := fmt.Sprintf("%s.config.inject[%d]", path, j)
		if inj.Domain == "" {
			errs.add(fieldErrorf(at+".domain", "capabilities[%d] (%s): inject[%d]: domain is required", i, x.Name, j))
		}
		if inj.Scheme != "" || inj.Username != "" {
			errs.add(fieldErrorf(at, "capabilities[%d] (%s): inject[%d]: an access token is presented as a header; scheme and username do not apply", i, x.Name, j))
		}
		if inj.Format != "" && !ContainsArgRef(inj.Format) && !ContainsEnvRef(inj.Format) && strings.Count(inj.Format, "%s") != 1 {
			errs.add(fieldErrorf(at+".format", "capabilities[%d] (%s): inject[%d]: format must contain exactly one %%s", i, x.Name, j))
		}
	}
	if err := errs.err(); err != nil {
		return nil, err
	}
	return &x, nil
}

// isIssuerIdentifier reports whether s is an authorization server issuer
// identifier as RFC 8414 defines one, held to https.
func isIssuerIdentifier(s string) bool {
	if len(s) > crossAppAccessMaxURL || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && !strings.Contains(s, "#")
}

// isResourceIndicator reports whether s is a resource indicator as
// RFC 8707 defines one: an absolute URI without a fragment.
func isResourceIndicator(s string) bool {
	if len(s) > crossAppAccessMaxURL || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.IsAbs() && u.Fragment == "" && !strings.Contains(s, "#")
}

// validateCrossAppAccessInject holds every grant's inject domains to the
// runtime allow list, as workload tokens are held, and to itself: a
// domain receives one runtime-held token, so a grant may not share one
// with another grant or with a workload token, where the request could
// not say whose authority it wants.
func validateCrossAppAccessInject(needs []Capability, invalid map[int]bool) error {
	var errs ValidationErrors
	policy, err := NetworkPolicyV2Of(needs)
	if err != nil {
		return err
	}
	allow := phaseAllow(policy, "runtime")
	owner := map[string]string{}
	for i, n := range needs {
		if n.Type != CapabilityWorkloadToken || invalid[i] {
			continue
		}
		var w WorkloadToken
		if err := DecodeCapabilityConfig(n, &w); err != nil {
			continue
		}
		for _, inj := range w.Inject {
			owner[strings.ToLower(inj.Domain)] = "workload token " + w.Name
		}
	}
	for i, n := range needs {
		if n.Type != CapabilityCrossAppAccess || invalid[i] {
			continue
		}
		var x CrossAppAccess
		if err := DecodeCapabilityConfig(n, &x); err != nil {
			errs.add(err)
			continue
		}
		self := "cross-app access " + x.Name
		for j, inj := range x.Inject {
			at := fmt.Sprintf("capabilities[%d].config.inject[%d].domain", i, j)
			if !allow[stripPort(inj.Domain)] && !allow["*"] && !allow["**"] {
				errs.add(fieldErrorf(at, "capabilities[%d] (%s): inject domain %q is not in the network policy's runtime allow list", i, x.Name, inj.Domain))
			}
			domain := strings.ToLower(inj.Domain)
			if prev, taken := owner[domain]; taken && prev != self {
				errs.add(fieldErrorf(at, "capabilities[%d] (%s): inject domain %q already receives %s; a domain takes one runtime-held token", i, x.Name, inj.Domain, prev))
				continue
			}
			owner[domain] = self
		}
	}
	return errs.err()
}

// InjectHeader returns the header an inject rule presents the access
// token in.
func (x CrossAppAccess) InjectHeader(inj Inject) string {
	if inj.Header == "" {
		return WorkloadTokenDefaultHeader
	}
	return inj.Header
}

// InjectFormat returns the format an inject rule renders the access
// token with.
func (x CrossAppAccess) InjectFormat(inj Inject) string {
	if inj.Format == "" {
		return WorkloadTokenDefaultFormat
	}
	return inj.Format
}
