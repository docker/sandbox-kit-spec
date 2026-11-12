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
	Kits              []string
	Claims            []string
	Rejected          []string
	Args              map[string]string
	WorkspaceAssigned bool
	Broken            string
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
	var units []*resolve.Unit
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
			raw, err = spec.ExpandCreateArgs(raw, d.Args, values)
			if err != nil {
				return err
			}
			d, err = spec.Decode(raw)
			if err != nil {
				return err
			}
		}
		if input.Broken == "ordering-flattens-scopes" {
			d.CapabilityScopes = nil
			clearScopes(d.Capabilities)
		}
		if input.Broken == "ordering-drops-skipped-edges" {
			removeSkippedEdges(d.Capabilities)
		}
		units = append(units, &resolve.Unit{Reference: kit, Descriptor: d})
	}
	dependencies, err := resolve.Dependencies(units, false)
	if err != nil {
		return err
	}
	var contributions []spec.Contribution
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
	var contracts map[string]spec.ExecutionContract
	if input.Broken == "ordering-applies-skipped" {
		available["test.invalid/unavailable@1"] = true
		contracts = map[string]spec.ExecutionContract{"test.invalid/unavailable@1": {}}
	}
	composition, err := spec.ComposeRuntime(context.Background(), contributions, spec.RuntimeOptions{
		Contracts:         contracts,
		WorkspaceAssigned: input.WorkspaceAssigned,
		Select: func(_ context.Context, _ spec.SelectionTarget, _ spec.Descriptor, c spec.Capability) spec.CapabilityDecision {
			return spec.CapabilityDecision{Accepted: available[c.Type]}
		},
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(composition.Plan)
}

func removeSkippedEdges(capabilities []spec.Capability) {
	for i := range capabilities {
		c := &capabilities[i]
		if c.Group != nil {
			for j := range c.Group.Capabilities {
				c.Group.Capabilities[j].Before = nil
				c.Group.Capabilities[j].After = nil
			}
			continue
		}
		c.Before = withoutBridge(c.Before)
		c.After = withoutBridge(c.After)
	}
}

func withoutBridge(targets []string) []string {
	var out []string
	for _, target := range targets {
		if target != "#bridge" {
			out = append(out, target)
		}
	}
	return out
}

func clearScopes(capabilities []spec.Capability) {
	for i := range capabilities {
		capabilities[i].Scope = nil
		if capabilities[i].Group != nil {
			clearScopes(capabilities[i].Group.Capabilities)
		}
	}
}
