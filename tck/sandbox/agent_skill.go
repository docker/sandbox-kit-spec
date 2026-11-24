package sandbox

import (
	"context"
	"errors"
	"slices"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const (
	capAgentSkill          = spec.CapabilityAgentSkill
	fixtureBundledSkill    = "bundled-skill"
	fixtureBundledOptional = "bundled-optional"
	fixtureBundledReader   = "bundled-reader"
	fixtureBundledExisting = "bundled-existing"
)

func bundledProbe(ctx context.Context, e *Env, id, mode, want string) []report.Finding {
	res, err := e.Adapter.Exec(ctx, id, "kit-tck-bundled", mode)
	if err != nil {
		return []report.Finding{report.Failf("%s: %v", mode, err)}
	}
	if res.ExitCode != 0 || res.Stdout != want {
		return []report.Finding{report.Failf("%s: exit %d, stdout %q, stderr %q; want %q", mode, res.ExitCode, res.Stdout, res.Stderr, want)}
	}
	return nil
}

func bundledObservation(mode, want string, shared bool) func(context.Context, *Env) []report.Finding {
	return func(ctx context.Context, e *Env) []report.Finding {
		kits := []string{fixtureBundledReader, fixtureBundledSkill}
		opts := adapter.CreateOptions{SkillsHostMode: "off"}
		if shared {
			opts.SkillsHostMode = "readonly"
		}
		id, cleanup, err := e.sandboxWith(ctx, kits, opts)
		if err != nil {
			return []report.Finding{report.Failf("create: %v", err)}
		}
		defer cleanup()
		findings := bundledProbe(ctx, e, id, mode, want)
		if shared {
			for _, dir := range []string{skillsReadOnlyPath, skillsWritablePath} {
				if f := storeMountedAt(ctx, e, id, dir); f != nil {
					findings = append(findings, f...)
				}
			}
		}
		return findings
	}
}

func bundledUnavailable(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureBundledSkill}, nil)
	cleanup()
	var refused *adapter.RefusedError
	if !errors.As(err, &refused) {
		return []report.Finding{report.Failf("required skill without a discovery directory: id=%q err=%v; want refusal", id, err)}
	}
	id, cleanup, err = e.sandbox(ctx, []string{fixtureWorkload, fixtureBundledOptional}, nil)
	if err != nil {
		return []report.Finding{report.Failf("optional skill without a discovery directory: %v", err)}
	}
	defer cleanup()
	state, err := e.Adapter.Selection(ctx, id)
	if err != nil {
		return []report.Finding{report.Failf("read skipped skill: %v", err)}
	}
	found := 0
	for _, r := range state.Selection.Selected {
		if r.Source != nil && (r.Source.Kit == fixtureBundledOptional || r.Source.Kit == e.Fixtures(fixtureBundledOptional)) {
			return []report.Finding{report.Failf("unavailable skill recorded as selected")}
		}
	}
	for _, r := range state.Selection.Skipped {
		if r.Source == nil || (r.Source.Kit != fixtureBundledOptional && r.Source.Kit != e.Fixtures(fixtureBundledOptional)) {
			continue
		}
		if r.Path != "capabilities[0]" || r.Source.Path != "capabilities[0]" || !slices.Equal(r.Members, []string{"capabilities[0]"}) || !slices.Equal(r.Rejected, []string{"capabilities[0]"}) || !slices.Equal(r.MemberSources, []spec.CapabilitySource{*r.Source}) {
			return []report.Finding{report.Failf("incomplete skipped skill record: %+v", r)}
		}
		found++
	}
	if found != 1 {
		return []report.Finding{report.Failf("got %d skipped skill records, want one", found)}
	}
	return nil
}

func bundledHostConflict(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandboxWith(ctx, []string{fixtureBundledReader, "bundled-host-conflict"}, adapter.CreateOptions{SkillsHostMode: "readonly"})
	cleanup()
	var refused *adapter.RefusedError
	if !errors.As(err, &refused) {
		return []report.Finding{report.Failf("host skill conflict: id=%q err=%v; want refusal", id, err)}
	}
	return nil
}

func bundledConflict(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{fixtureBundledReader, fixtureBundledSkill, fixtureBundledExisting}, nil)
	cleanup()
	var refused *adapter.RefusedError
	if !errors.As(err, &refused) {
		return []report.Finding{report.Failf("existing skill conflict: id=%q err=%v; want refusal", id, err)}
	}
	return nil
}

var bundledSkillChecks = []check{
	{requirement: "agent-skills@1/off-default-honored", capability: capAgentSkills, run: skillsModeOff("default")},
	{requirement: "agent-skills@1/off-honored", capability: capAgentSkills, run: skillsModeOff("off")},
	{requirement: "agent-skills@1/bundled-with-sharing-off", capability: capAgentSkills, needs: []string{capAgentSkill}, run: bundledWithSharingOff},
	{requirement: "agent-skills@1/host-store-missing", capability: capAgentSkills, run: skillsWithoutStore("missing")},
	{requirement: "agent-skills@1/host-store-empty", capability: capAgentSkills, run: skillsWithoutStore("empty")},
	{requirement: "agent-skills@1/bundled-with-missing-store", capability: capAgentSkills, needs: []string{capAgentSkill}, run: bundledWithoutStore("missing")},
	{requirement: "agent-skills@1/bundled-with-empty-store", capability: capAgentSkills, needs: []string{capAgentSkill}, run: bundledWithoutStore("empty")},
	{requirement: "agent-skill@1/host-conflict", capability: capAgentSkill, needs: []string{capAgentSkills}, run: bundledHostConflict},
	{requirement: "agent-skill@1/exposed", capability: capAgentSkill, needs: []string{capAgentSkills}, run: bundledObservation("exposed", "ready\n", false)},
	{requirement: "agent-skill@1/before-launch", capability: capAgentSkill, needs: []string{capAgentSkills}, run: bundledObservation("launch", "ready\n", false)},
	{requirement: "agent-skill@1/no-execution", capability: capAgentSkill, needs: []string{capAgentSkills}, run: bundledObservation("no-execution", "clean\n", false)},
	{requirement: "agent-skill@1/unavailable", capability: capAgentSkill, run: bundledUnavailable},
	{requirement: "agent-skill@1/existing-conflict", capability: capAgentSkill, needs: []string{capAgentSkills}, run: bundledConflict},
	{requirement: "agent-skills@1/destination", capability: capAgentSkills, needs: []string{capAgentSkill}, run: bundledObservation("exposed", "ready\n", true)},
}

func init() { checks = append(checks, bundledSkillChecks...) }

// No host content must not erase the destination from capability selection.
func skillsWithoutHost(fixture, path string, opts adapter.CreateOptions) func(context.Context, *Env) []report.Finding {
	return func(ctx context.Context, e *Env) []report.Finding {
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, fixture}, opts)
		if err != nil {
			return []report.Finding{report.Failf("host skills are unavailable, but the skills destination must remain available: %v", err)}
		}
		defer cleanup()
		res, err := e.Adapter.Exec(ctx, id, "ls", path)
		if err != nil {
			return []report.Finding{report.Failf("probe host store: %v", err)}
		}
		if res.ExitCode == 0 && listsExactly(res.Stdout, SkillName) {
			return []report.Finding{report.Failf("host skills are unavailable, but shared skills are visible at %s", path)}
		}
		state, err := e.Adapter.Selection(ctx, id)
		if err != nil {
			return []report.Finding{report.Failf("read skills selection: %v", err)}
		}
		matches := func(r spec.SelectionRecord) bool {
			return r.Source != nil && (r.Source.Kit == fixture || r.Source.Kit == e.Fixtures(fixture)) && r.Source.Path == "capabilities[0]"
		}
		for _, r := range state.Selection.Skipped {
			if matches(r) {
				return []report.Finding{report.Failf("host skills are unavailable, but the skills destination must not be skipped")}
			}
		}
		for _, r := range state.Selection.Selected {
			if matches(r) {
				return nil
			}
		}
		return []report.Finding{report.Failf("skills destination missing from selected capabilities")}
	}
}

// Exercise both selection forms without requiring bundled-skill support.
func skillsWithoutStore(store string) func(context.Context, *Env) []report.Finding {
	return func(ctx context.Context, e *Env) []report.Finding {
		opts := adapter.CreateOptions{SkillsHostStore: store}
		findings := skillsWithoutHost(fixtureSkills, skillsReadOnlyPath, opts)(ctx, e)
		return append(findings, skillsWithoutHost(fixtureSkillsOptional, "/home/agent/.kit-tck/skills-opt", opts)(ctx, e)...)
	}
}

func bundledWithoutStore(store string) func(context.Context, *Env) []report.Finding {
	return func(ctx context.Context, e *Env) []report.Finding {
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureBundledReader, fixtureBundledSkill}, adapter.CreateOptions{SkillsHostStore: store})
		if err != nil {
			return []report.Finding{report.Failf("create with %s host store: %v", store, err)}
		}
		defer cleanup()
		findings := bundledProbe(ctx, e, id, "exposed", "ready\n")
		return append(findings, bundledProbe(ctx, e, id, "launch", "ready\n")...)
	}
}

// A permissive host makes the Kit's refusal of host sharing observable.
// Both selection forms remain available without asking for host content.
func skillsModeOff(mode string) func(context.Context, *Env) []report.Finding {
	return func(ctx context.Context, e *Env) []report.Finding {
		findings := skillsWithoutHost("skills-"+mode, skillsReadOnlyPath, adapter.CreateOptions{})(ctx, e)
		return append(findings, skillsWithoutHost("skills-optional-"+mode, "/home/agent/.kit-tck/skills-opt", adapter.CreateOptions{})(ctx, e)...)
	}
}

// A bundled-only destination remains useful with an enabled, populated
// host store, including the optional destination with explicit off.
func bundledWithSharingOff(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{"bundled-reader-off", fixtureBundledSkill}, nil)
	if err != nil {
		return []report.Finding{report.Failf("create with Kit sharing off: %v", err)}
	}
	defer cleanup()
	findings := bundledProbe(ctx, e, id, "exposed", "ready\n")
	findings = append(findings, bundledProbe(ctx, e, id, "launch", "ready\n")...)
	for _, path := range []string{skillsReadOnlyPath, skillsWritablePath} {
		res, err := e.Adapter.Exec(ctx, id, "ls", path)
		if err != nil {
			findings = append(findings, report.Failf("probe host store: %v", err))
			continue
		}
		if res.ExitCode == 0 && listsExactly(res.Stdout, SkillName) {
			findings = append(findings, report.Failf("Kit sharing is off, but host skills are visible at %s", path))
		}
	}
	return findings
}
