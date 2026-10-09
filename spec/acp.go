package spec

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// ACP is CapabilityACP's config. Command is a complete stdio adapter
// invocation, unlike the tails in the workload's session capabilities.
type ACP struct {
	Agent           string            `json:"agent" yaml:"agent"`
	Command         CommandLine       `json:"command" yaml:"command"`
	Env             map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	ProtocolVersion *int              `json:"protocolVersion,omitempty" yaml:"protocolVersion,omitempty"`
}

// ACPProtocolVersion returns the declared major version or its default.
func ACPProtocolVersion(a ACP) int {
	if a.ProtocolVersion == nil {
		return 1
	}
	return *a.ProtocolVersion
}

// ACPCapability retains the selection and display metadata of an adapter.
type ACPCapability struct {
	ACP
	Optional    bool
	Name        string
	Description string
}

// ACPsOf returns selected adapter declarations in declaration order.
// Callers match Agent against the composed provides, not just the declaring
// Kit's provides: an adapter mixin can front another Kit's agent.
func ACPsOf(capabilities []Capability) ([]ACPCapability, error) {
	var out []ACPCapability
	for _, c := range capabilities {
		if c.Group != nil {
			return nil, fmt.Errorf("ACP: select groups first")
		}
		if c.Type != CapabilityACP {
			continue
		}
		var a ACP
		if err := DecodeCapabilityConfig(c, &a); err != nil {
			return nil, err
		}
		out = append(out, ACPCapability{ACP: a, Optional: c.Optional, Name: c.Name, Description: c.Description})
	}
	return out, nil
}

func sameACP(a, b ACP) bool {
	return NormalizeCapabilityName(a.Agent) == NormalizeCapabilityName(b.Agent) &&
		slices.Equal(a.Command, b.Command) && maps.Equal(a.Env, b.Env) &&
		ACPProtocolVersion(a) == ACPProtocolVersion(b)
}

// Presence rules run even when a placeholder elsewhere defers decoding.
// JSON decodes null strings as empty strings, losing the authored error.
func validateACPAuthored(path string, i int, n Capability) error {
	var errs ValidationErrors
	for _, key := range []string{"agent", "command", "env", "protocolVersion"} {
		value, stated := n.Config[key]
		if !stated && (key == "agent" || key == "command") {
			errs.add(fieldErrorf(path+".config."+key, "capabilities[%d]: %s is required", i, key))
		} else if stated && hasNull(value) {
			errs.add(fieldErrorf(path+".config."+key, "capabilities[%d]: %s must not be null or contain null", i, key))
		}
	}
	if emptyCommand(n.Config["command"]) {
		errs.add(fieldErrorf(path+".config.command", "capabilities[%d]: command must name a command", i))
	}
	if value, stated := n.Config["env"]; stated && !hasNull(value) {
		rv, _ := deref(reflect.ValueOf(value))
		if rv.Kind() == reflect.Map {
			iter := rv.MapRange()
			for iter.Next() {
				key := fmt.Sprint(iter.Key().Interface())
				if !envVarName.MatchString(key) {
					errs.add(fieldErrorf(path+".config.env", "capabilities[%d]: invalid environment variable %q", i, key))
				}
				if hasNull(iter.Value().Interface()) {
					errs.add(fieldErrorf(path+".config.env", "capabilities[%d]: env values must be strings, not null", i))
				}
			}
		}
	}
	return errs.err()
}

func validateACP(path string, i int, n Capability) error {
	var errs ValidationErrors
	errs.add(validateACPAuthored(path, i, n))
	var a ACP
	if err := DecodeCapabilityConfig(n, &a); err != nil {
		errs.add(fieldErrorf(path+".config", "capabilities[%d]: %v", i, err))
		return errs.err()
	}
	if err := capabilityNameError(a.Agent); err != nil {
		errs.add(fieldErrorf(path+".config.agent", "capabilities[%d]: agent must be an unversioned provides name: %v", i, err))
	}
	if ACPProtocolVersion(a) < 1 {
		errs.add(fieldErrorf(path+".config.protocolVersion", "capabilities[%d]: protocolVersion must be positive", i))
	}
	for _, arg := range a.Command {
		if strings.ContainsRune(arg, '\x00') {
			errs.add(fieldErrorf(path+".config.command", "capabilities[%d]: command contains NUL", i))
		}
	}
	for key, value := range a.Env {
		if strings.ContainsRune(value, '\x00') {
			errs.add(fieldErrorf(path+".config.env", "capabilities[%d]: env value for %q contains NUL", i, key))
		}
	}
	return errs.err()
}
