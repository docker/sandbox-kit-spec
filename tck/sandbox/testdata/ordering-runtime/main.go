package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/docker/sandbox-kit-spec/v3/resolve"
	"github.com/docker/sandbox-kit-spec/v3/spec"
)

type request struct {
	Kits     []string
	Claims   []string
	Rejected []string
	Args     map[string]string
}

type step struct {
	EntryID    string              `json:"entryID"`
	Stage      spec.ExecutionStage `json:"stage"`
	Action     string              `json:"action"`
	Order      int32               `json:"order"`
	Capability spec.Capability     `json:"capability"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run() error {
	var input request
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		return err
	}
	var contributions []spec.Contribution
	for _, kit := range input.Kits {
		raw, err := os.ReadFile(filepath.Join(kit, filepath.Base(kit)+".yaml"))
		if err != nil {
			return err
		}
		d, err := spec.Decode(raw)
		if err != nil {
			return err
		}
		if len(d.Args) > 0 {
			values, err := spec.KitArgValues(d.Args, input.Args)
			if err != nil {
				return err
			}
			expanded, err := spec.ExpandCreateArgs(raw, d.Args, values)
			if err != nil {
				return err
			}
			d, err = spec.Decode(expanded)
			if err != nil {
				return err
			}
		}
		contributions = append(contributions, spec.Contribution{Reference: kit, Descriptor: d})
	}
	units := make([]*resolve.Unit, len(contributions))
	for i, contribution := range contributions {
		units[i] = &resolve.Unit{Reference: contribution.Reference, Descriptor: contribution.Descriptor}
	}
	dependencies, err := resolve.Dependencies(units, false)
	if err != nil {
		return err
	}
	contributions = nil
	for _, unit := range dependencies.Topological() {
		contributions = append(contributions, spec.Contribution{Reference: unit.Reference, Descriptor: unit.Descriptor})
	}
	available := map[string]bool{}
	for _, typ := range input.Claims {
		available[typ] = true
	}
	for _, typ := range input.Rejected {
		available[typ] = false
	}
	composition, err := spec.ComposeRuntime(context.Background(), contributions, spec.RuntimeOptions{
		Select: func(_ context.Context, _ spec.SelectionTarget, _ spec.Descriptor, c spec.Capability) spec.CapabilityDecision {
			return spec.CapabilityDecision{Accepted: available[c.Type]}
		},
	})
	if err != nil {
		return err
	}
	steps := []step{}
	for _, stage := range composition.Plan.Stages {
		for _, action := range stage.Steps {
			steps = append(steps, step{action.EntryID, stage.Stage, action.Action, action.Order, action.Capability})
		}
	}
	return json.NewEncoder(os.Stdout).Encode(steps)
}
