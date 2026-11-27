package sandbox

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

// Withdrawal covers the device only: CAP_SYS_ADMIN may stay for
// privileged@1 or dockerd@1, so its absence is not evidence of withdrawal.
func fuseDeviceUsable(ctx context.Context, e *Env, id string, want bool) []report.Finding {
	got, f := execOutput(ctx, e, id, "kit-tck-fuse", "device")
	if f != nil {
		return []report.Finding{*f}
	}
	// Only an explicit answer counts: empty or garbled output is no evidence
	// that the device went away.
	expected := map[bool]string{true: "yes", false: "no"}[want]
	if got = strings.TrimSpace(got); got != expected {
		return []report.Finding{report.Failf("kit-tck-fuse device reports %q, expected %q", got, expected)}
	}
	return nil
}

// A granted state is the whole grant: a runtime that restores /dev/fuse but
// drops the bounding-set bit or blocks fusermount3 has not granted FUSE.
// Withdrawal stays device-only, for the reason fuseDeviceUsable gives.
func fuseAccess(ctx context.Context, e *Env, id string, want bool) []report.Finding {
	if findings := fuseDeviceUsable(ctx, e, id, want); len(findings) > 0 || !want {
		return findings
	}
	for _, argv := range [][]string{{"kit-tck-fuse", "cap"}, {"kit-tck-fuse-mount"}} {
		got, f := execOutput(ctx, e, id, argv...)
		if f != nil {
			return []report.Finding{*f}
		}
		if got = strings.TrimSpace(got); got != "yes" {
			return []report.Finding{report.Failf("FUSE granted, but %s reports %q", strings.Join(argv, " "), got)}
		}
	}
	return nil
}

// The page lets a runtime refuse FUSE; any other failed create is a failure.
func fuseCreateFinding(err error) []report.Finding {
	if err == nil {
		return nil
	}
	var refused *adapter.RefusedError
	if errors.As(err, &refused) {
		return []report.Finding{report.Skipf("runtime refused the FUSE request: %s", refused.Detail)}
	}
	return []report.Finding{report.Failf("create FUSE sandbox: %v", err)}
}

// Recreation is the protocol's observable composition change. A refusal
// must preserve the prior access; a success must finish before it is
// reported, withdrawal of the device included. Withdrawal can be asserted
// because conformance.md has the adapter withhold operator FUSE grants for
// these fixtures.
func fuseTransition(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, "fuse-first"}, nil)
	defer cleanup()
	if findings := fuseCreateFinding(err); len(findings) > 0 {
		return findings
	}
	if findings := fuseAccess(ctx, e, id, true); len(findings) > 0 {
		return findings
	}
	for _, want := range []bool{false, true} {
		before, selectionErr := e.Adapter.Selection(ctx, id)
		if selectionErr != nil {
			return []report.Finding{report.Failf("previous FUSE composition: %v", selectionErr)}
		}
		fixture := "fuse-absent"
		if want {
			fixture = "fuse-first"
		}
		err = e.Adapter.RecreateWith(ctx, id, []string{e.Fixtures(fixtureWorkload), e.Fixtures(fixture)}, nil)
		var refused *adapter.RefusedError
		if errors.As(err, &refused) {
			after, selectionErr := e.Adapter.Selection(ctx, id)
			if selectionErr != nil || !reflect.DeepEqual(before, after) {
				return []report.Finding{report.Failf("refused FUSE change altered the previous composition: before=%+v after=%+v error=%v", before, after, selectionErr)}
			}
			if findings := fuseAccess(ctx, e, id, !want); len(findings) > 0 {
				return findings
			}
			return []report.Finding{report.Skipf("FUSE composition change refused with previous access preserved: %s", refused.Detail)}
		}
		if err != nil {
			return []report.Finding{report.Failf("recreate with FUSE=%t: %v", want, err)}
		}
		state, selectionErr := e.Adapter.Selection(ctx, id)
		if selectionErr != nil {
			return []report.Finding{report.Failf("changed FUSE surface: %v", selectionErr)}
		}
		if slices.Contains(state.Surface.Services, capFUSE) != want {
			return []report.Finding{report.Failf("recreation reported the wrong FUSE grant: %+v", state.Surface)}
		}
		if findings := fuseAccess(ctx, e, id, want); len(findings) > 0 {
			return findings
		}
	}
	return nil
}

func fuseIndependent(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, "fuse-first", "fuse-second"}, nil)
	defer cleanup()
	if findings := fuseCreateFinding(err); len(findings) > 0 {
		return findings
	}
	if findings := fuseAccess(ctx, e, id, true); len(findings) > 0 {
		return findings
	}
	before, selectionErr := e.Adapter.Selection(ctx, id)
	if selectionErr != nil {
		return []report.Finding{report.Failf("initial independent FUSE grants: %v", selectionErr)}
	}
	err = e.Adapter.RecreateWith(ctx, id, []string{e.Fixtures(fixtureWorkload), e.Fixtures("fuse-second")}, nil)
	var refused *adapter.RefusedError
	if err != nil && !errors.As(err, &refused) {
		return []report.Finding{report.Failf("remove one FUSE contributor: %v", err)}
	}
	after, selectionErr := e.Adapter.Selection(ctx, id)
	if selectionErr != nil {
		return []report.Finding{report.Failf("remaining independent FUSE grant: %v", selectionErr)}
	}
	if errors.As(err, &refused) && !reflect.DeepEqual(before, after) {
		return []report.Finding{report.Failf("refused contributor removal altered the previous composition: before=%+v after=%+v", before, after)}
	}
	if err == nil {
		// Continued access alone also fits a recreation that ignored the
		// replacement composition. Credit removal only after observing it.
		hasSource := func(kit string) bool {
			return slices.ContainsFunc(after.Selection.Selected, func(record spec.SelectionRecord) bool {
				return record.Source != nil && (record.Source.Kit == kit || record.Source.Kit == e.Fixtures(kit))
			})
		}
		if hasSource("fuse-first") || !hasSource("fuse-second") {
			return []report.Finding{report.Failf("successful recreation did not replace the FUSE contributors: %+v", after.Selection)}
		}
	}
	if !slices.Contains(after.Surface.Services, capFUSE) {
		return []report.Finding{report.Failf("removing one contributor withdrew the remaining FUSE grant: %+v", after.Surface)}
	}
	if findings := fuseAccess(ctx, e, id, true); len(findings) > 0 {
		return findings
	}
	if err != nil {
		return []report.Finding{report.Skipf("contributor removal refused with previous composition preserved: %s", refused.Detail)}
	}
	return nil
}

func init() {
	checks = append(checks,
		check{requirement: "fuse@1/grant-transition", capability: capFUSE, run: fuseTransition},
		check{requirement: "fuse@1/independent-grants", capability: capFUSE, run: fuseIndependent},
	)
}
