package sandbox

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const (
	orderingInstall = "A,R,R,C,"
	orderingStartup = "A1,A2,B1,B2,C,"
)

func init() {
	checks = append(checks,
		check{requirement: "SPEC-v3 §7.1.1/selection-before-application", capability: capLifecycle, needs: []string{capHostPort}, run: orderedSelectionRefusal},
		check{requirement: "SPEC-v3 §7.1.1/conflicts-before-application", capability: capLifecycle, needs: []string{capHostPort}, run: orderedConflictRefusal},
		check{requirement: "SPEC-v3 §7.1.3/stable-order", capability: capLifecycle, run: orderedLifecycle},
		check{requirement: "SPEC-v3 §7.1.3/target-resolution", capability: capLifecycle, run: orderedTargets},
		check{requirement: "SPEC-v3 §7.1.3/written-edges", capability: capLifecycle, needs: []string{capHostPort}, run: orderedCycleRefusal},
		check{requirement: "SPEC-v3 §7.1.3/default-edges", capability: capDockerd, needs: []string{capLifecycle}, run: orderedDocker},
		check{requirement: "lifecycle@1/hooks-dependency-order", capability: capLifecycle, run: orderedDependencies},
		check{requirement: "SPEC-v3 §12/phase-barriers", capability: capLifecycle, run: orderedBarriers},
		check{requirement: "SPEC-v3 §12/step-barriers", capability: capLifecycle, run: orderedBarriers},
		check{requirement: "SPEC-v3 §12/repetition", capability: capLifecycle, run: orderedLifetime},
		check{requirement: "SPEC-v3 §12/readiness", capability: capDockerd, needs: []string{capLifecycle}, run: orderedDocker},
		check{requirement: "SPEC-v3 §12/reverse-stop", capability: capLifecycle, needs: []string{capDockerd}, run: orderedStop},
		check{requirement: "SPEC-v3 §12/workspace-ready", capability: capLifecycle, needs: []string{capSbx}, run: orderedWorkspace},
		check{requirement: "SPEC-v3 §12/workspace-failure", capability: capLifecycle, needs: []string{capSbx, capHostPort}, run: orderedWorkspaceFailure},
		check{requirement: "lifecycle@1/hook-failure", capability: capLifecycle, needs: []string{capHostPort}, run: orderedHookFailure},
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
	id, cleanup, err := e.sandbox(ctx, []string{"ordering-workload", "ordering"}, nil)
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("ordered lifecycle create: %v", err)}
	}
	for _, observation := range []struct{ file, want string }{
		{"/var/tmp/ordering-install", orderingInstall},
		{"/var/tmp/ordering-startup", orderingStartup},
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

func orderedBarriers(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{"ordering-workload", "ordering"}, nil)
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("phase observation create: %v", err)}
	}
	return orderingValue(ctx, e, id, "/var/tmp/ordering-entrypoint", orderingInstall+"|"+orderingStartup+"|file,\n")
}

func orderedLifetime(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{"ordering-workload", "ordering"}, nil)
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("ordered lifecycle create: %v", err)}
	}
	res, err := e.Adapter.Exec(ctx, id, "rm", "/var/tmp/ordering-file")
	if err != nil || res.ExitCode != 0 {
		return []report.Finding{report.Failf("remove start-time file before reboot: %v", err)}
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
		{"/var/tmp/ordering-file", "file,"},
	} {
		if f := orderingValue(ctx, e, id, observation.file, observation.want); f != nil {
			return f
		}
	}
	return nil
}

func orderedDependencies(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{"ordering-workload", "ordering-requirer", "ordering-provider"}, nil)
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("dependency-ordered hooks create: %v", err)}
	}
	for _, phase := range []string{"install", "startup"} {
		if f := orderingValue(ctx, e, id, "/var/tmp/ordering-dependencies-"+phase, "provider,requirer,"); f != nil {
			return f
		}
	}
	return nil
}

func orderedTargets(ctx context.Context, e *Env) []report.Finding {
	for _, kits := range [][]string{
		{"ordering-workload", "ordering-requirer", "ordering-insert", "ordering-provider"},
		{"ordering-workload", "ordering-requirer", "ordering-published"},
	} {
		id, cleanup, err := e.sandbox(ctx, kits, nil)
		defer cleanup()
		if err != nil {
			return []report.Finding{report.Failf("qualified target create: %v", err)}
		}
		if f := orderingValue(ctx, e, id, "/var/tmp/ordering-dependencies-startup", "insert,provider,requirer,"); f != nil {
			return f
		}
	}
	return nil
}

func orderedDocker(ctx context.Context, e *Env) []report.Finding {
	control, removeControl, findings := dockerSandbox(ctx, e, []string{"ordering-workload", "ordering-docker-default"})
	defer removeControl()
	if findings != nil {
		return findings
	}
	if f := orderingValue(ctx, e, control, "/var/tmp/ordering-ping", "OK,"); f != nil {
		return f
	}
	id, cleanup, err := e.sandbox(ctx, []string{"ordering-workload", "ordering-docker"}, nil)
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("engine accepted by control but configuration-before-engine composition failed: %v", err)}
	}
	for boot := range 2 {
		if f := orderingValue(ctx, e, id, "/var/tmp/ordering-ping", strings.Repeat("OK,", boot+1)); f != nil {
			return f
		}
		if f := orderingValue(ctx, e, id, "/var/tmp/ordering-config-observed", strings.Repeat(`{"ordering":"from-lifecycle"}`, boot+1)); f != nil {
			return f
		}
		if boot == 0 {
			if err := e.Adapter.Stop(ctx, id); err != nil {
				return []report.Finding{report.Failf("stop: %v", err)}
			}
			if err := e.Adapter.Start(ctx, id); err != nil {
				return []report.Finding{report.Failf("start: %v", err)}
			}
		}
	}
	return nil
}

func orderedStop(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, findings := dockerSandbox(ctx, e, []string{"ordering-workload", "ordering-stop"})
	defer cleanup()
	if findings != nil {
		return findings
	}
	if err := e.Adapter.Stop(ctx, id); err != nil {
		return []report.Finding{report.Failf("stop programs: %v", err)}
	}
	if err := e.Adapter.Start(ctx, id); err != nil {
		return []report.Finding{report.Failf("restart to read shutdown record: %v", err)}
	}
	return orderingValue(ctx, e, id, "/var/tmp/ordering-stop", "workload,B:ready,A:ready,engine,")
}

func orderedSelectionRefusal(ctx context.Context, e *Env) []report.Finding {
	return orderedRefusalTrace(ctx, e, "ordering-refusal", "", adapter.CreateOptions{RejectCapabilities: []string{capLifecycle}}, true)
}

func orderedConflictRefusal(ctx context.Context, e *Env) []report.Finding {
	return orderedRefusalTrace(ctx, e, "ordering-refusal", "ordering-conflict", adapter.CreateOptions{}, true)
}

func orderedCycleRefusal(ctx context.Context, e *Env) []report.Finding {
	return orderedRefusalTrace(ctx, e, "ordering-refusal", "ordering-cycle", adapter.CreateOptions{}, true)
}

func orderedHookFailure(ctx context.Context, e *Env) []report.Finding {
	return orderedRefusalTrace(ctx, e, "ordering-failure", "", adapter.CreateOptions{}, false)
}

func orderedWorkspaceFailure(ctx context.Context, e *Env) []report.Finding {
	source, _, cleanup, err := orderingWorkspaceSource(ctx)
	if err != nil {
		return []report.Finding{report.Failf("prepare workspace source: %v", err)}
	}
	defer cleanup()
	return orderedRefusalTrace(ctx, e, "ordering-workspace-failure", "", adapter.CreateOptions{WorkspaceMode: "cloned", WorkspaceSource: source}, false)
}

func orderedRefusalTrace(ctx context.Context, e *Env, fixture, additional string, opts adapter.CreateOptions, requireRefusal bool) []report.Finding {
	service, err := newHostPortService()
	if err != nil {
		return []report.Finding{report.Failf("listen for forbidden hook effects: %v", err)}
	}
	defer service.start()()
	rejected := opts.RejectCapabilities
	opts.RejectCapabilities = nil
	opts.Args = map[string]string{"host_port": service.port()}
	kits := []string{"ordering-workload", fixture}
	control, removeControl, err := e.sandboxWith(ctx, kits, opts)
	defer removeControl()
	if sbxWorkspaceUnavailable(err, opts.WorkspaceMode) {
		return []report.Finding{report.Skipf("runtime adapter cannot arrange cloned workspaces")}
	}
	if err != nil {
		return []report.Finding{report.Failf("trace control create: %v", err)}
	}
	if f := orderingValue(ctx, e, control, "/var/tmp/ordering-refusal", service.token); f != nil {
		return f
	}
	before := service.requests.Load()
	if before == 0 {
		return []report.Finding{report.Failf("trace control hook did not reach the host")}
	}
	if len(rejected) != 0 {
		opts.RejectCapabilities = rejected
	} else if additional != "" {
		kits = append(kits, additional)
	} else if opts.WorkspaceSource != "" {
		opts.WorkspaceSource = filepath.Join(opts.WorkspaceSource, "missing-repository")
	} else {
		opts.Args["fail"] = "true"
	}
	_, cleanup, createErr := e.sandboxWith(ctx, kits, opts)
	defer cleanup()
	var refused *adapter.RefusedError
	if createErr == nil || (requireRefusal && !errors.As(createErr, &refused)) {
		return []report.Finding{report.Failf("invalid or failed prerequisite did not stop create: %v", createErr)}
	}
	if service.requests.Load() != before {
		return []report.Finding{report.Failf("a lifecycle hook reached the host before its prerequisite completed")}
	}
	return nil
}

func orderedWorkspace(ctx context.Context, e *Env) []report.Finding {
	source, token, cleanupSource, err := orderingWorkspaceSource(ctx)
	if err != nil {
		return []report.Finding{report.Failf("prepare workspace source: %v", err)}
	}
	defer cleanupSource()
	id, cleanup, err := e.sandboxWith(ctx, []string{"ordering-workload", "ordering-workspace"}, adapter.CreateOptions{WorkspaceMode: "cloned", WorkspaceSource: source})
	defer cleanup()
	if sbxWorkspaceUnavailable(err, "cloned") {
		return []report.Finding{report.Skipf("runtime adapter cannot arrange cloned workspaces")}
	}
	if err != nil {
		return []report.Finding{report.Failf("cloned workspace create: %v", err)}
	}
	for boot := range 2 {
		if f := orderingValue(ctx, e, id, "/var/tmp/ordering-workspace", token+"executable"); f != nil {
			return f
		}
		installed, finding := execOutput(ctx, e, id, "sh", "-c", `cat "$WORKSPACE_DIR/installed"`)
		if finding != nil {
			return []report.Finding{*finding}
		}
		if installed != "install" {
			return []report.Finding{report.Failf("workspace install marker: got %q, want install", installed)}
		}
		if boot == 0 {
			if err := e.Adapter.Stop(ctx, id); err != nil {
				return []report.Finding{report.Failf("stop cloned workspace: %v", err)}
			}
			if err := e.Adapter.Start(ctx, id); err != nil {
				return []report.Finding{report.Failf("restart cloned workspace: %v", err)}
			}
		}
	}
	return nil
}

func orderingWorkspaceSource(ctx context.Context) (string, string, func(), error) {
	dir, err := os.MkdirTemp("", "kit-tck-workspace-")
	if err != nil {
		return "", "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	token := rand.Text()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		cleanup()
		return "", "", nil, err
	}
	for name, body := range map[string]string{"nested/marker": token, "check": "#!/bin/sh\nprintf executable\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			cleanup()
			return "", "", nil, err
		}
	}
	for _, args := range [][]string{
		{"init", "--quiet"}, {"add", "--", "nested/marker", "check"},
		{"-c", "user.name=Kit TCK", "-c", "user.email=kit-tck@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture source"},
	} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			cleanup()
			return "", "", nil, fmt.Errorf("git workspace source: %w: %s", err, output)
		}
	}
	return dir, token, cleanup, nil
}
