package sandbox

import (
	"context"
	"errors"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const capAgentContextV2 = spec.CapabilityAgentContextV2

func inlineContextMaterialized(ctx context.Context, e *Env) []report.Finding {
	for _, explicit := range []bool{false, true} {
		kits := []string{"context-v2-tool", "context-v2-workload"}
		profiles := []string{ProfilePath}
		snapshots := []string{"/var/tmp/context-v2-at-launch-0"}
		want := "base-context-v2\n\ntool-context-v2 built platform runtime-team $TEAM ${TEAM}"
		if explicit {
			kits = append([]string{"context-v2-profile"}, kits...)
			profiles = []string{"/home/agent/.kit-tck/v2/AGENTS.md", "/home/agent/.kit-tck/v2-other/OTHER.md"}
			want += "\n\nprofile-context-v2"
			snapshots = []string{"/var/tmp/context-v2-at-launch-1", "/var/tmp/context-v2-at-launch-2"}
		}
		id, cleanup, err := e.sandboxWith(ctx, kits, adapter.CreateOptions{Args: map[string]string{"team": "platform"}, Env: map[string]string{"KIT_CONTEXT_TEAM": "runtime-team"}})
		if err != nil {
			cleanup()
			return []report.Finding{report.Failf("inline context create: %v", err)}
		}
		for _, file := range append(profiles, snapshots...) {
			body, f := execOutput(ctx, e, id, "cat", file)
			if f != nil {
				cleanup()
				return []report.Finding{*f}
			}
			if !strings.Contains(body, want) || !strings.Contains(body, "existing-context-v2") {
				cleanup()
				return []report.Finding{report.Failf("%s omits ordered inline guidance or existing instructions: %q", file, body)}
			}
		}
		cleanup()
	}
	return nil
}

func inlineContextSelection(ctx context.Context, e *Env) []report.Finding {
	for _, reject := range []bool{false, true} {
		opts := adapter.CreateOptions{}
		if reject {
			opts.RejectCapabilities = []string{spec.CapabilityVolume}
		}
		id, cleanup, err := e.sandboxWith(ctx, []string{"context-v2-workload", "context-v2-tool", "context-v2-profile", "context-v2-optional"}, opts)
		if err != nil {
			cleanup()
			return []report.Finding{report.Failf("inline selection create: %v", err)}
		}
		for _, file := range []string{"/home/agent/.kit-tck/v2/AGENTS.md", "/home/agent/.kit-tck/v2-other/OTHER.md"} {
			body, f := execOutput(ctx, e, id, "cat", file)
			if f != nil {
				cleanup()
				return []report.Finding{*f}
			}
			if !strings.Contains(body, "base-context-v2") || !strings.Contains(body, "tool-context-v2") || strings.Contains(body, "optional-context-v2") == reject {
				cleanup()
				return []report.Finding{report.Failf("%s contains the wrong selected guidance: %q", file, body)}
			}
		}
		optional, err := e.Adapter.Exec(ctx, id, "cat", "/home/agent/.kit-tck/v2-optional/OPTIONAL.md")
		cleanup()
		if err != nil || (optional.ExitCode == 0) == reject || (!reject && !strings.Contains(optional.Stdout, "base-context-v2")) {
			return []report.Finding{report.Failf("optional profile does not follow selection or lacks shared guidance: %+v (%v)", optional, err)}
		}
	}
	return nil
}

func inlineContextMixed(ctx context.Context, e *Env) []report.Finding {
	_, cleanup, err := e.sandbox(ctx, []string{"context-v2-workload", fixtureContext}, nil)
	cleanup()
	var refused *adapter.RefusedError
	if !errors.As(err, &refused) {
		return []report.Finding{report.Failf("selected @1 and @2 must refuse explicitly: %v", err)}
	}
	return nil
}

func init() {
	checks = append(checks,
		check{requirement: "agent-context@2/materialized", capability: capAgentContextV2, run: inlineContextMaterialized},
		check{requirement: "agent-context@2/selection-boundaries", capability: capAgentContextV2, needs: []string{spec.CapabilityVolume}, run: inlineContextSelection},
		check{requirement: "agent-context@2/mixed-versions", capability: capAgentContextV2, needs: []string{capAgentContext}, run: inlineContextMixed},
	)
}
