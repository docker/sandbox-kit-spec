package sandbox

import (
	"context"
	"errors"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const (
	orderingInstall = "A,R,R,C,"
	orderingStartup = "A1,A2,A3,B1,B2,C,"
)

func init() {
	checks = append(checks,
		check{requirement: "SPEC-v3 §7.1.2/reconciled-order", capability: capVolume, run: orderedReconciliation},
		check{requirement: "lifecycle@1/hooks-dependency-order", capability: capLifecycle, run: orderedDependencies},
		check{requirement: "SPEC-v3 §7.1.2/no-partial-application", capability: capDockerd, needs: []string{capLifecycle, capHostPort}, run: orderedNoEffectsBeforeRefusal},
		check{requirement: "SPEC-v3 §7.1.2/numeric-order", capability: capLifecycle, run: orderedLifecycle},
		check{requirement: "SPEC-v3 §7.1.2/stage-lifetime", capability: capLifecycle, run: orderedLifetime},
		check{requirement: "SPEC-v3 §7.1.2/readiness", capability: capDockerd, needs: []string{capLifecycle}, run: orderedDocker},
		check{requirement: "SPEC-v3 §7.1.2/prerequisites", capability: capDockerd, needs: []string{capLifecycle}, run: orderedContradiction},
	)
}

func orderingValue(ctx context.Context, e *Env, id, file, want string) []report.Finding {
	got, f := execOutput(ctx, e, id, "cat", file)
	if f != nil {
		return []report.Finding{*f}
	}
	if got != want {
		return []report.Finding{report.Failf("%s: got %q, want %q", file, got, want)}
	}
	return nil
}

func orderedLifecycle(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, "ordering"}, nil)
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("ordered lifecycle create: %v", err)}
	}
	for _, observation := range []struct{ file, want string }{
		{"/var/tmp/ordering-install", orderingInstall},
		{"/var/tmp/ordering-startup", orderingStartup},
		{"/var/tmp/ordering-observed", "file,"},
	} {
		if f := orderingValue(ctx, e, id, observation.file, observation.want); f != nil {
			return f
		}
	}
	res, err := e.Adapter.Exec(ctx, id, "test", "-e", "/var/tmp/ordering-skipped")
	if err != nil || res.ExitCode == 0 {
		return []report.Finding{report.Failf("unselected group produced a lifecycle file: %v", err)}
	}
	return nil
}

func orderedLifetime(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, "ordering"}, nil)
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("ordered lifecycle create: %v", err)}
	}
	if err := e.Adapter.Stop(ctx, id); err != nil {
		return []report.Finding{report.Failf("stop: %v", err)}
	}
	if err := e.Adapter.Start(ctx, id); err != nil {
		return []report.Finding{report.Failf("start: %v", err)}
	}
	for _, observation := range []struct{ file, want string }{
		{"/var/tmp/ordering-install", orderingInstall},
		{"/var/tmp/ordering-startup", orderingStartup + orderingStartup},
		{"/var/tmp/ordering-observed", "file,file,"},
	} {
		if f := orderingValue(ctx, e, id, observation.file, observation.want); f != nil {
			return f
		}
	}
	if err := e.Adapter.Recreate(ctx, id); err != nil {
		return []report.Finding{report.Failf("recreate: %v", err)}
	}
	if f := orderingValue(ctx, e, id, "/var/tmp/ordering-startup", orderingStartup); f != nil {
		return f
	}
	res, err := e.Adapter.Exec(ctx, id, "test", "-e", "/var/tmp/ordering-install")
	if err != nil || res.ExitCode == 0 {
		return []report.Finding{report.Failf("container replacement reran completed install actions: %v", err)}
	}
	fresh, removeFresh, err := e.sandbox(ctx, []string{fixtureWorkload, "ordering"}, nil)
	defer removeFresh()
	if err != nil {
		return []report.Finding{report.Failf("fresh sandbox create: %v", err)}
	}
	return orderingValue(ctx, e, fresh, "/var/tmp/ordering-install", orderingInstall)
}

func orderedDocker(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, f := dockerSandbox(ctx, e, []string{"ordering-workload", "ordering-docker"})
	defer cleanup()
	if f != nil {
		return f
	}
	for boot := range 3 {
		want := strings.Repeat("OK,", boot+1)
		if boot == 2 {
			want = "OK,"
		}
		if f := orderingValue(ctx, e, id, "/var/tmp/ordering-ping", want); f != nil {
			return f
		}
		config, finding := execOutput(ctx, e, id, "cat", "/var/tmp/ordering-config-observed")
		if finding != nil {
			return []report.Finding{*finding}
		}
		if strings.Count(config, `"ordering":"from-lifecycle"`) != strings.Count(want, "OK,") {
			return []report.Finding{report.Failf("engine did not consume the lifecycle configuration before startup hooks: %q", config)}
		}
		switch boot {
		case 0:
			if err := e.Adapter.Stop(ctx, id); err != nil {
				return []report.Finding{report.Failf("stop: %v", err)}
			}
			if err := e.Adapter.Start(ctx, id); err != nil {
				return []report.Finding{report.Failf("start: %v", err)}
			}
		case 1:
			if err := e.Adapter.Recreate(ctx, id); err != nil {
				return []report.Finding{report.Failf("recreate: %v", err)}
			}
		}
	}
	return nil
}

func orderedContradiction(ctx context.Context, e *Env) []report.Finding {
	_, cleanup, err := e.sandbox(ctx, []string{"ordering-workload", "ordering-invalid"}, nil)
	defer cleanup()
	var refused *adapter.RefusedError
	if !errors.As(err, &refused) {
		return []report.Finding{report.Failf("startup ordered before mandatory Docker readiness was not refused: %v", err)}
	}
	return nil
}

func orderedNoEffectsBeforeRefusal(ctx context.Context, e *Env) []report.Finding {
	service, err := newHostPortService()
	if err != nil {
		return []report.Finding{report.Failf("listen for forbidden hook effects: %v", err)}
	}
	defer service.start()()
	args := map[string]string{"host_port": service.port(), "auto_start": "false"}
	control, removeControl, err := e.sandbox(ctx, []string{"ordering-workload", "ordering-invalid"}, args)
	defer removeControl()
	if err != nil {
		return []report.Finding{report.Failf("trace control create: %v", err)}
	}
	if f := orderingValue(ctx, e, control, "/var/tmp/ordering-contradiction", service.token); f != nil {
		return f
	}
	before := service.requests.Load()
	if before == 0 {
		return []report.Finding{report.Failf("trace control hook did not reach the host")}
	}
	args["auto_start"] = "true"
	_, cleanup, createErr := e.sandbox(ctx, []string{"ordering-workload", "ordering-invalid"}, args)
	defer cleanup()
	var refused *adapter.RefusedError
	if !errors.As(createErr, &refused) {
		return []report.Finding{report.Failf("contradictory order was not refused: %v", createErr)}
	}
	if service.requests.Load() != before {
		return []report.Finding{report.Failf("a lifecycle hook reached the host before invalid ordering was refused")}
	}
	return nil
}

func orderedDependencies(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, "ordering-requirer", "ordering-provider"}, nil)
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("dependency-ordered hooks create: %v", err)}
	}
	for _, stage := range []string{"install", "startup"} {
		if f := orderingValue(ctx, e, id, "/var/tmp/ordering-dependencies-"+stage, "provider,requirer,"); f != nil {
			return f
		}
	}
	if err := e.Adapter.Stop(ctx, id); err != nil {
		return []report.Finding{report.Failf("stop: %v", err)}
	}
	if err := e.Adapter.Start(ctx, id); err != nil {
		return []report.Finding{report.Failf("start: %v", err)}
	}
	return orderingValue(ctx, e, id, "/var/tmp/ordering-dependencies-startup", "provider,requirer,provider,requirer,")
}

func orderedReconciliation(ctx context.Context, e *Env) []report.Finding {
	_, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, "ordering-volume-first", "ordering-volume-second"}, nil)
	defer cleanup()
	var refused *adapter.RefusedError
	if !errors.As(err, &refused) {
		return []report.Finding{report.Failf("same volume requested at different orders was not refused: %v", err)}
	}
	for _, contributor := range []string{"ordering-volume-first", "ordering-volume-second"} {
		if !strings.Contains(refused.Detail, contributor) {
			return []report.Finding{report.Failf("order conflict did not name contributor %s: %s", contributor, refused.Detail)}
		}
	}
	return nil
}
