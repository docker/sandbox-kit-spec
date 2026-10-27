package spec

// phasesOf reads a credential or ssh-agent entry's phase independently of
// parameterized siblings, so a deferred boolean or service cannot hide
// invalid literal phases.
func phasesOf(n Capability) (Phases, error) {
	var c struct {
		Phase Phases `json:"phase"`
	}
	err := DecodeCapabilityConfig(Capability{Type: n.Type, Config: map[string]any{"phase": n.Config["phase"]}}, &c)
	return c.Phase, err
}

func validatePhases(path string, i int, phases Phases) error {
	if len(phases) == 0 {
		return fieldErrorf(path+".config.phase", "capabilities[%d]: phase must be install or runtime, or a non-empty list of distinct phases", i)
	}
	seen := map[string]bool{}
	for _, phase := range phases {
		if !ContainsArgRef(phase) && !ContainsEnvRef(phase) && phase != "install" && phase != "runtime" {
			return fieldErrorf(path+".config.phase", "capabilities[%d]: phase must be install or runtime, got %q", i, phase)
		}
		if seen[phase] {
			return fieldErrorf(path+".config.phase", "capabilities[%d]: phase %q listed twice", i, phase)
		}
		seen[phase] = true
	}
	return nil
}
