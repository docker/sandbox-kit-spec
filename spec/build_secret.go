package spec

import (
	"maps"
	"slices"

	"gopkg.in/yaml.v3"
)

// Empty fields normally decode to an Arg's zero values. For secret inputs,
// accepting an empty ID as absence would turn a misspelled declaration into
// an ordinary, persisted input. Inspect authored field presence as well.
func validateRawBuildSecrets(raw []byte, d *Descriptor) error {
	if len(d.Args) == 0 {
		return nil
	}
	var authored struct {
		Args map[string]map[string]any `yaml:"args"`
	}
	if err := yaml.Unmarshal(raw, &authored); err != nil {
		// ValidateRaw also accepts caller-built descriptors without valid
		// source bytes; Decode owns source syntax errors on the build path.
		return nil
	}
	var errs ValidationErrors
	for _, name := range slices.Sorted(maps.Keys(authored.Args)) {
		fields := authored.Args[name]
		if _, present := fields["buildSecret"]; !present {
			continue
		}
		arg, declared := d.Args[name]
		if !declared {
			continue
		}
		// Published-set validation can substitute the set's declarations
		// before rewriting the member's source. Judge only matching inputs.
		if id, ok := fields["buildSecret"].(string); ok && id != arg.BuildSecret {
			continue
		}
		path := "args." + name
		if arg.BuildSecret == "" {
			errs.add(fieldErrorf(path+".buildSecret", "buildSecret must be a non-empty secret ID"))
			continue
		}
		// Nonzero conflicting fields are already reported by validateArgs.
		for _, field := range []struct {
			name  string
			empty bool
		}{
			{"env", arg.Env == ""}, {"buildArg", arg.BuildArg == ""},
			{"default", arg.Default == nil}, {"enum", len(arg.Enum) == 0}, {"pattern", arg.Pattern == ""},
		} {
			if _, present := fields[field.name]; present && field.empty {
				errs.add(fieldErrorf(path+"."+field.name, "buildSecret excludes %s, including an empty value", field.name))
			}
		}
	}
	return errs.err()
}
