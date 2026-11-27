package sandbox

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const capDisplay = spec.CapabilityDisplay

func displayProbe(ctx context.Context, e *Env, id, want string) []report.Finding {
	output, finding := execOutput(ctx, e, id, "kit-tck-display", "/run/display/wayland-0")
	if finding != nil {
		return []report.Finding{*finding}
	}
	if got := strings.TrimSpace(output); got != want {
		return []report.Finding{report.Failf("display socket: got %q, want %q", got, want)}
	}
	return nil
}

func displaySocket(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{"display-workload", "display"}, nil)
	defer cleanup()
	var refused *adapter.RefusedError
	if errors.As(err, &refused) && diagnosticNames(refused.Detail, capDisplay) {
		return []report.Finding{report.Skipf("runtime refused display: %s", refused.Detail)}
	}
	if err != nil {
		return []report.Finding{report.Failf("create display sandbox: %v", err)}
	}
	return displayProbe(ctx, e, id, "usable")
}

func displayNoGrant(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{"display-workload"}, nil)
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("create sandbox without display: %v", err)}
	}
	return displayProbe(ctx, e, id, "unavailable")
}

func displayRequiredRefused(ctx context.Context, e *Env) []report.Finding {
	_, cleanup, err := e.sandboxWith(ctx, []string{"display-workload", "display"}, adapter.CreateOptions{RejectCapabilities: []string{capDisplay}})
	defer cleanup()
	var refused *adapter.RefusedError
	if !errors.As(err, &refused) || !diagnosticNames(refused.Detail, capDisplay) {
		return []report.Finding{report.Failf("expected required display refusal naming %s, got %v", capDisplay, err)}
	}
	return nil
}

func displayOptionalSkipped(ctx context.Context, e *Env) []report.Finding {
	const fixture = "display-optional"
	id, cleanup, err := e.sandboxWith(ctx, []string{"display-workload", fixture}, adapter.CreateOptions{RejectCapabilities: []string{capDisplay}})
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("create sandbox with unavailable optional display: %v", err)}
	}
	state, err := e.Adapter.Selection(ctx, id)
	if err != nil {
		return []report.Finding{report.Failf("read optional display selection: %v", err)}
	}
	// The workload has no declarations; only the optional display can be recorded.
	if len(state.Selection.Selected) != 0 || len(state.Selection.Skipped) != 1 || slices.Contains(state.Surface.Services, capDisplay) {
		return []report.Finding{report.Failf("expected only a skipped display request and no display grant, got %+v", state)}
	}
	record := state.Selection.Skipped[0]
	const path = "capabilities[0]"
	members := []string{path}
	if record.Path != path || record.Source == nil || record.Source.Path != path ||
		(record.Source.Kit != fixture && record.Source.Kit != e.Fixtures(fixture)) ||
		!slices.Equal(record.Members, members) || !slices.Equal(record.Rejected, members) ||
		!slices.Equal(record.MemberSources, []spec.CapabilitySource{*record.Source}) {
		return []report.Finding{report.Failf("incomplete optional display skip record: %+v", record)}
	}
	return displayProbe(ctx, e, id, "unavailable")
}
