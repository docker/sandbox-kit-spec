package sandbox

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const exactVersionRequirement = "SPEC-v3 §7.1.2/exact-version"

type exactContextRequest struct {
	version  int
	optional bool
}

func (r exactContextRequest) fixture() string {
	need := "required"
	if r.optional {
		need = "optional"
	}
	return fmt.Sprintf("exact-context-v%d-%s", r.version, need)
}

func (r exactContextRequest) capability() string {
	return fmt.Sprintf("com.docker.sandbox/agent-context@%d", r.version)
}

func exactVersionSelection(ctx context.Context, e *Env) []report.Finding {
	requests := [][]exactContextRequest{
		{{1, false}}, {{1, true}}, {{2, false}}, {{2, true}},
		{{1, true}, {2, true}}, {{1, false}, {2, true}},
		{{1, true}, {2, false}}, {{1, false}, {2, false}},
	}
	for _, authored := range requests {
		for _, rejected := range [][]string{nil, {capAgentContext}, {capAgentContextV2}, {capAgentContext, capAgentContextV2}} {
			if findings := exactContextSelection(ctx, e, authored, rejected, ""); len(findings) > 0 {
				return findings
			}
		}
	}
	for _, pair := range []struct {
		fixture     string
		lowRequired bool
	}{{"exact-context-pair-optional", false}, {"exact-context-pair-required-low", true}} {
		for _, rejected := range [][]string{nil, {capAgentContext}, {capAgentContextV2}, {capAgentContext, capAgentContextV2}} {
			authored := []exactContextRequest{{1, !pair.lowRequired}, {2, true}}
			if findings := exactContextSelection(ctx, e, authored, rejected, pair.fixture); len(findings) > 0 {
				return findings
			}
		}
	}
	return nil
}

func exactContextSelection(ctx context.Context, e *Env, authored []exactContextRequest, rejected []string, pairFixture string) []report.Finding {
	kits := []string{"exact-context-workload"}
	fixtureFor := func(request exactContextRequest) string {
		if pairFixture != "" {
			return pairFixture
		}
		return request.fixture()
	}
	pathFor := func(request exactContextRequest) string {
		if pairFixture != "" {
			return fmt.Sprintf("capabilities[%d]", request.version-1)
		}
		return "capabilities[0]"
	}
	var selected, denied []exactContextRequest
	requiredRefusal := false
	for _, request := range authored {
		if !slices.Contains(kits, fixtureFor(request)) {
			kits = append(kits, fixtureFor(request))
		}
		if e.claims(request.capability()) && !slices.Contains(rejected, request.capability()) {
			selected = append(selected, request)
		} else {
			denied = append(denied, request)
			requiredRefusal = requiredRefusal || !request.optional
		}
	}
	id, cleanup, err := e.sandboxWith(ctx, kits, adapter.CreateOptions{RejectCapabilities: rejected})
	defer cleanup()
	if requiredRefusal || len(selected) > 1 {
		var refused *adapter.RefusedError
		if !errors.As(err, &refused) {
			return []report.Finding{report.Failf("%v rejected %v must refuse exact requests: %v", kits, rejected, err)}
		}
		identifies := func(request exactContextRequest) bool {
			sourceNamed := strings.Contains(refused.Detail, fixtureFor(request)) || strings.Contains(refused.Detail, e.Fixtures(fixtureFor(request)))
			return (e.claims(request.capability()) || strings.Contains(refused.Detail, request.capability())) && sourceNamed && strings.Contains(refused.Detail, pathFor(request))
		}
		if requiredRefusal {
			if !slices.ContainsFunc(denied, func(request exactContextRequest) bool {
				return !request.optional && identifies(request)
			}) {
				return []report.Finding{report.Failf("exact-version refusal must identify a rejected required request: %s", refused.Detail)}
			}
		} else {
			for _, request := range selected {
				if !identifies(request) {
					return []report.Finding{report.Failf("exact-version conflict must identify the request %s %s %s: %s", request.capability(), fixtureFor(request), pathFor(request), refused.Detail)}
				}
			}
		}
		return nil
	}
	if err != nil {
		return []report.Finding{report.Failf("%v rejected %v should create: %v", kits, rejected, err)}
	}
	state, err := e.Adapter.Selection(ctx, id)
	if err != nil {
		return []report.Finding{report.Failf("exact-version selection records: %v", err)}
	}
	for _, request := range authored {
		accepted := slices.Contains(selected, request)
		records, unexpected := state.Selection.Skipped, state.Selection.Selected
		if accepted {
			records, unexpected = unexpected, records
		}
		found := false
		for _, record := range records {
			if record.Path == pathFor(request) && record.Source != nil && (record.Source.Kit == fixtureFor(request) || record.Source.Kit == e.Fixtures(fixtureFor(request))) {
				found = record.Source.Path == pathFor(request) && slices.Equal(record.Members, []string{pathFor(request)})
				if accepted {
					found = found && len(record.Rejected) == 0
				} else {
					found = found && slices.Equal(record.Rejected, []string{pathFor(request)})
				}
			}
		}
		for _, record := range unexpected {
			if record.Path == pathFor(request) && record.Source != nil && (record.Source.Kit == fixtureFor(request) || record.Source.Kit == e.Fixtures(fixtureFor(request))) {
				found = false
			}
		}
		if !found {
			return []report.Finding{report.Failf("%s lost independent exact-version selection, accepted=%t: %+v", fixtureFor(request), accepted, state.Selection)}
		}
	}
	profile, err := e.Adapter.Exec(ctx, id, "cat", "/home/agent/.kit-tck/exact/AGENTS.md")
	if err != nil {
		return []report.Finding{report.Failf("exact-version profile: %v", err)}
	}
	if len(selected) == 0 {
		if profile.ExitCode == 0 {
			return []report.Finding{report.Failf("skipped exact requests materialized a profile: %q", profile.Stdout)}
		}
		return nil
	}
	request := selected[0]
	want := "inline-guidance-version-two"
	if request.version == 1 {
		want = StagedKitRoot + "/" + fixtureFor(request) + "/context.txt"
	}
	if profile.ExitCode != 0 || !strings.Contains(profile.Stdout, want) || (request.version == 1 && strings.Contains(profile.Stdout, "inline-guidance-version-two")) {
		return []report.Finding{report.Failf("%s must retain its authored version's profile behavior, want %q: %+v", fixtureFor(request), want, profile)}
	}
	for _, request := range denied {
		unwanted := []string{"inline-guidance-version-two"}
		if request.version == 1 {
			unwanted = []string{StagedKitRoot + "/" + fixtureFor(request) + "/context.txt", "legacy-guidance-version-one"}
		}
		for _, marker := range unwanted {
			if strings.Contains(profile.Stdout, marker) {
				return []report.Finding{report.Failf("skipped %s %s contributed guidance %q: %s", fixtureFor(request), pathFor(request), marker, profile.Stdout)}
			}
		}
	}
	return nil
}

func init() {
	checks = append(checks, check{requirement: exactVersionRequirement, run: exactVersionSelection})
}
