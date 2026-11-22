package sandbox

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const capKVM = spec.CapabilityKVM

// kvmAccess observes each device independently. An all-or-nothing probe would
// miss a withdrawn grant that left just one of the two interfaces usable.
func kvmAccess(ctx context.Context, e *Env, id string, want bool) []report.Finding {
	for _, device := range []string{"kvm", "vsock"} {
		result, err := e.Adapter.Exec(ctx, id, "kit-tck-kvm", device)
		if err != nil {
			return []report.Finding{report.Failf("probe %s: %v", device, err)}
		}
		usable := result.ExitCode == 0 && strings.TrimSpace(result.Stdout) == "usable"
		unavailable := result.ExitCode == 3 && strings.HasPrefix(result.Stderr, "unavailable:"+device+":")
		if (want && !usable) || (!want && !unavailable) {
			return []report.Finding{report.Failf("%s usable=%t, expected %t: %s", device, result.ExitCode == 0, want, result.Stderr)}
		}
	}
	return nil
}

// Deliberate hardware or policy refusal is permitted. A crashed create or a
// generic error is not evidence that the runtime rejected this capability.
func kvmCreateFinding(err error) []report.Finding {
	if err == nil {
		return nil
	}
	var refused *adapter.RefusedError
	if errors.As(err, &refused) && diagnosticNames(refused.Detail, capKVM) {
		return []report.Finding{report.Skipf("runtime refused KVM access: %s", refused.Detail)}
	}
	return []report.Finding{report.Failf("create KVM sandbox: %v", err)}
}

func kvmDevices(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, "kvm"}, nil)
	defer cleanup()
	if findings := kvmCreateFinding(err); len(findings) > 0 {
		return findings
	}
	return kvmAccess(ctx, e, id, true)
}

func kvmBeforeStartup(ctx context.Context, e *Env) []report.Finding {
	// Workload startup is observable without hook support. Only add the
	// hook fixture and its capture when the runtime claims lifecycle.
	fixture := "kvm"
	records := []string{"/var/tmp/kit-tck-workload-kvm"}
	if e.claims(capLifecycle) {
		fixture = "kvm-hooks"
		records = append([]string{"/var/tmp/kit-tck-kvm-hook"}, records...)
	}
	id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixture}, nil)
	defer cleanup()
	if findings := kvmCreateFinding(err); len(findings) > 0 {
		return findings
	}
	for boot := range 2 {
		if boot > 0 {
			// Captures survive stop/start. Remove them so the next boot must
			// prove access before its own hooks and workload execute.
			if _, finding := execOutput(ctx, e, id, append([]string{"rm", "-f"}, records...)...); finding != nil {
				return []report.Finding{*finding}
			}
			if err = e.Adapter.Stop(ctx, id); err != nil {
				return []report.Finding{report.Failf("stop startup-probe sandbox: %v", err)}
			}
			if err = e.Adapter.Start(ctx, id); err != nil {
				return []report.Finding{report.Failf("restart startup-probe sandbox: %v", err)}
			}
		}
		// A running container can precede its entrypoint's atomic publication.
		waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		for _, record := range records {
			for {
				got, finding := execOutput(waitCtx, e, id, "cat", record)
				if finding == nil {
					if strings.TrimSpace(got) != "usable" {
						return []report.Finding{report.Failf("KVM was not usable at startup (%s): %q", record, got)}
					}
					break
				}
				select {
				case <-waitCtx.Done():
					return []report.Finding{report.Failf("read KVM startup capture %s: %s", record, finding.Detail)}
				case <-time.After(50 * time.Millisecond):
				}
			}
		}
	}

	return kvmAccess(ctx, e, id, true)
}

func kvmRequiredRefused(ctx context.Context, e *Env) []report.Finding {
	_, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, "kvm"}, adapter.CreateOptions{RejectCapabilities: []string{capKVM}})
	defer cleanup()
	var refused *adapter.RefusedError
	if !errors.As(err, &refused) {
		return []report.Finding{report.Failf("unavailable required KVM must be deliberately refused: %v", err)}
	}
	if !diagnosticNames(refused.Detail, capKVM) {
		return []report.Finding{report.Failf("required refusal must name %s: %s", capKVM, refused.Detail)}
	}
	return nil
}

func kvmOptionalSkipped(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandboxWith(ctx, []string{fixtureWorkload, "kvm-optional"}, adapter.CreateOptions{RejectCapabilities: []string{capKVM}})
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("unavailable optional KVM must permit creation: %v", err)}
	}
	state, err := e.Adapter.Selection(ctx, id)
	if err != nil {
		return []report.Finding{report.Failf("optional KVM selection: %v", err)}
	}
	matches := func(record spec.SelectionRecord) bool {
		return record.Source != nil && (record.Source.Kit == "kvm-optional" || record.Source.Kit == e.Fixtures("kvm-optional"))
	}
	found := false
	for _, record := range state.Selection.Skipped {
		if matches(record) && record.Path == "capabilities[0]" && record.Source.Path == record.Path && slices.Equal(record.Members, []string{record.Path}) && slices.Equal(record.Rejected, []string{record.Path}) {
			found = true
		}
	}
	if !found || slices.ContainsFunc(state.Selection.Selected, matches) || slices.Contains(state.Surface.Services, capKVM) {
		return []report.Finding{report.Failf("optional KVM lacks an accurate skipped record and effective surface: %+v", state)}
	}
	return kvmAccess(ctx, e, id, false)
}

func kvmRestart(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, "kvm"}, nil)
	defer cleanup()
	if findings := kvmCreateFinding(err); len(findings) > 0 {
		return findings
	}
	if findings := kvmAccess(ctx, e, id, true); len(findings) > 0 {
		return findings
	}
	before, err := e.Adapter.Selection(ctx, id)
	if err != nil {
		return []report.Finding{report.Failf("initial KVM selection: %v", err)}
	}
	// Changed selector answers affect future selection, never this accepted boot.
	if err = e.Adapter.RejectCapabilities(ctx, id, capKVM); err != nil {
		return []report.Finding{report.Failf("set future KVM rejection: %v", err)}
	}
	if err = e.Adapter.Stop(ctx, id); err != nil {
		return []report.Finding{report.Failf("stop: %v", err)}
	}
	if err = e.Adapter.Start(ctx, id); err != nil {
		return []report.Finding{report.Failf("start: %v", err)}
	}
	after, err := e.Adapter.Selection(ctx, id)
	if err != nil {
		return []report.Finding{report.Failf("restarted KVM selection: %v", err)}
	}
	if !reflect.DeepEqual(before, after) {
		return []report.Finding{report.Failf("restart changed accepted KVM selection: before=%+v after=%+v", before, after)}
	}
	return kvmAccess(ctx, e, id, true)
}

// Recreation is the protocol's observable composition-change operation.
// Refusal must preserve the prior access; successful changes must finish
// before the operation reports success, including withdrawal of each device.
func kvmTransition(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, "kvm"}, nil)
	defer cleanup()
	if findings := kvmCreateFinding(err); len(findings) > 0 {
		return findings
	}
	if findings := kvmAccess(ctx, e, id, true); len(findings) > 0 {
		return findings
	}
	for _, want := range []bool{false, true} {
		before, selectionErr := e.Adapter.Selection(ctx, id)
		if selectionErr != nil {
			return []report.Finding{report.Failf("previous KVM composition: %v", selectionErr)}
		}
		fixture := "kvm-absent"
		if want {
			fixture = "kvm"
		}
		err = e.Adapter.RecreateWith(ctx, id, []string{e.Fixtures(fixtureWorkload), e.Fixtures(fixture)}, nil)
		var refused *adapter.RefusedError
		if errors.As(err, &refused) {
			after, selectionErr := e.Adapter.Selection(ctx, id)
			if selectionErr != nil || !reflect.DeepEqual(before, after) {
				return []report.Finding{report.Failf("refused KVM change altered the previous composition: before=%+v after=%+v error=%v", before, after, selectionErr)}
			}
			if findings := kvmAccess(ctx, e, id, !want); len(findings) > 0 {
				return findings
			}
			return []report.Finding{report.Skipf("KVM composition change refused with previous access preserved: %s", refused.Detail)}
		}
		if err != nil {
			return []report.Finding{report.Failf("recreate with KVM=%t: %v", want, err)}
		}
		state, selectionErr := e.Adapter.Selection(ctx, id)
		if selectionErr != nil {
			return []report.Finding{report.Failf("changed KVM surface: %v", selectionErr)}
		}
		if slices.Contains(state.Surface.Services, capKVM) != want {
			return []report.Finding{report.Failf("recreation reported the wrong KVM grant: %+v", state.Surface)}
		}
		if findings := kvmAccess(ctx, e, id, want); len(findings) > 0 {
			return findings
		}
	}
	return nil
}

func kvmIndependent(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, "kvm", "kvm-second"}, nil)
	defer cleanup()
	if findings := kvmCreateFinding(err); len(findings) > 0 {
		return findings
	}
	if findings := kvmAccess(ctx, e, id, true); len(findings) > 0 {
		return findings
	}
	before, selectionErr := e.Adapter.Selection(ctx, id)
	if selectionErr != nil {
		return []report.Finding{report.Failf("initial independent KVM grants: %v", selectionErr)}
	}
	err = e.Adapter.RecreateWith(ctx, id, []string{e.Fixtures(fixtureWorkload), e.Fixtures("kvm-second")}, nil)
	var refused *adapter.RefusedError
	if err != nil && !errors.As(err, &refused) {
		return []report.Finding{report.Failf("remove one KVM contributor: %v", err)}
	}
	after, selectionErr := e.Adapter.Selection(ctx, id)
	if selectionErr != nil {
		return []report.Finding{report.Failf("remaining independent KVM grant: %v", selectionErr)}
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
		if hasSource("kvm") || !hasSource("kvm-second") {
			return []report.Finding{report.Failf("successful recreation did not replace the KVM contributors: %+v", after.Selection)}
		}
	}
	if !slices.Contains(after.Surface.Services, capKVM) {
		return []report.Finding{report.Failf("removing one contributor withdrew the remaining KVM grant: %+v", after.Surface)}
	}
	if findings := kvmAccess(ctx, e, id, true); len(findings) > 0 {
		return findings
	}
	if err != nil {
		return []report.Finding{report.Skipf("contributor removal refused with previous composition preserved: %s", refused.Detail)}
	}
	return nil
}

func init() {
	checks = append(checks,
		check{requirement: "kvm@1/devices-usable", capability: capKVM, run: kvmDevices},
		check{requirement: "kvm@1/before-startup", capability: capKVM, run: kvmBeforeStartup},
		check{requirement: "kvm@1/required-unsatisfiable-refused", capability: capKVM, run: kvmRequiredRefused},
		check{requirement: "kvm@1/optional-unavailable-skipped", capability: capKVM, run: kvmOptionalSkipped},
		check{requirement: "kvm@1/restart", capability: capKVM, run: kvmRestart},
		check{requirement: "kvm@1/grant-transition", capability: capKVM, run: kvmTransition},
		check{requirement: "kvm@1/independent-grants", capability: capKVM, run: kvmIndependent},
	)
}
