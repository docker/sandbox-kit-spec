package spec

// validateWorkloadIdentityNeed decodes and checks one workload-identity
// entry. An empty config is valid: presence alone asks for an identity on
// the runtime's defaults.
func validateWorkloadIdentityNeed(path string, i int, n Capability) (*WorkloadIdentity, error) {
	var w WorkloadIdentity
	if err := DecodeCapabilityConfig(n, &w); err != nil {
		return nil, configErrorf(path, i, err)
	}
	if err := validateEnum(path+".config.trailers", i, "workload-identity trailers", w.Trailers,
		WorkloadIdentityTrailersFull, WorkloadIdentityTrailersShort); err != nil {
		return nil, err
	}
	if err := validateEnum(path+".config.ledger", i, "workload-identity ledger", w.Ledger,
		WorkloadIdentityLedgerRequired, WorkloadIdentityLedgerPreferred); err != nil {
		return nil, err
	}
	return &w, nil
}

// validateEnum accepts an omitted value, a reference the host resolves at
// create, or one of the listed spellings.
func validateEnum(path string, i int, label, value string, allowed ...string) error {
	if value == "" || ContainsArgRef(value) || ContainsEnvRef(value) {
		return nil
	}
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fieldErrorf(path, "capabilities[%d]: %s must be one of %s, got %q", i, label, joinQuoted(allowed), value)
}

func joinQuoted(values []string) string {
	out := ""
	for i, v := range values {
		switch {
		case i == 0:
		case i == len(values)-1:
			out += " or "
		default:
			out += ", "
		}
		out += `"` + v + `"`
	}
	return out
}

// MergeWorkloadIdentities folds two requests for one sandbox, keeping the
// stricter answer on each axis: full trailers over short, a required ledger
// over a preferred one. The kit that asked for more decides.
func MergeWorkloadIdentities(a, b WorkloadIdentity) WorkloadIdentity {
	return WorkloadIdentity{
		Trailers: stricter(a.Trailers, b.Trailers, WorkloadIdentityTrailersFull, WorkloadIdentityTrailersShort),
		Ledger:   stricter(a.Ledger, b.Ledger, WorkloadIdentityLedgerRequired, WorkloadIdentityLedgerPreferred),
	}
}

// stricter picks the value earlier in order, which lists spellings from
// strictest to loosest; an omitted value yields to a stated one.
func stricter(a, b string, order ...string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	for _, v := range order {
		if a == v || b == v {
			return v
		}
	}
	return a
}
