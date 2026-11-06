package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"maps"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const (
	fixtureSbxEnvironment = "sbx-environment"
	sbxEnvironmentProbe   = "kit-tck-sbx-env"
	sbxWorkspace          = "/home/sbxagent/project"
)

var sbxEnvironmentNames = []string{"WORKSPACE_DIR", "SANDBOX_NAME", "SANDBOX_ID", "IS_SANDBOX"}

func sbxConflictingEnvironment(value string) map[string]string {
	env := map[string]string{}
	for _, name := range sbxEnvironmentNames {
		env[name] = value
	}
	return env
}

func sbxReadEnvironment(ctx context.Context, e *Env, id string, argv ...string) (map[string]string, *report.Finding) {
	body, f := execOutput(ctx, e, id, append([]string{sbxEnvironmentProbe}, argv...)...)
	if f != nil {
		return nil, f
	}
	return sbxDecodeEnvironment(body)
}

func sbxDecodeEnvironment(body string) (map[string]string, *report.Finding) {
	var env map[string]string
	if err := json.Unmarshal([]byte(body), &env); err != nil || env == nil {
		return nil, failing("environment probe did not return an object of strings")
	}
	return env, nil
}

func sbxValidateEnvironment(env map[string]string, name, mode string) *report.Finding {
	if env["SANDBOX_NAME"] != name || env["SANDBOX_ID"] == "" || env["IS_SANDBOX"] != "1" {
		return failing("workload did not receive its assigned sandbox name, a non-empty ID, and IS_SANDBOX=1")
	}
	for _, value := range env {
		if strings.Contains(value, HostSentinelValue) {
			return failing("host workspace or sandbox identity reached the workload")
		}
	}
	workspace, present := env["WORKSPACE_DIR"]
	if mode == "none" {
		if present {
			return failing("WORKSPACE_DIR is present without an assigned workspace")
		}
	} else if (mode != "" || present) && workspace != sbxWorkspace {
		return failing("WORKSPACE_DIR does not identify the workspace at the absolute image WORKDIR")
	}
	return nil
}

// A requested mode may be unsupported, but the ordinary create path is
// always observable for a runtime claiming sbx@1. Refusing that path fails.
func sbxWorkspaceUnavailable(err error, mode string) bool {
	var refused *adapter.RefusedError
	return mode != "" && errors.As(err, &refused) && refused.Detail == "unsupported workspace mode: "+mode
}

func sbxEnvironment(ctx context.Context, e *Env, mode string) []report.Finding {
	for _, input := range []string{"clean", "image", "argument", "override", "empty"} {
		kits := []string{fixtureSbxEnvironment}
		opts := adapter.CreateOptions{Name: "kit-tck-env-" + strings.ToLower(rand.Text()), WorkspaceMode: mode}
		if input == "clean" {
			kits[0] = "sbx-environment-clean"
		}
		if input != "image" && input != "clean" {
			kits = append(kits, "sbx-environment-exports")
		}
		if input == "override" {
			opts.Env = sbxConflictingEnvironment("runtime-conflict")
		}
		if input == "empty" {
			opts.Env = sbxConflictingEnvironment("")
		}
		id, cleanup, err := e.sandboxWith(ctx, kits, opts)
		if err != nil {
			cleanup()
			if input == "clean" && sbxWorkspaceUnavailable(err, mode) {
				return []report.Finding{report.Skipf("workspace mode %s unavailable: %v", mode, err)}
			}
			return []report.Finding{report.Failf("create for %s inputs: %v", input, err)}
		}
		findings := sbxObserveEnvironment(ctx, e, id, opts.Name, mode)
		cleanup()
		if len(findings) != 0 {
			return findings
		}
	}
	return nil
}

func sbxObserveEnvironment(ctx context.Context, e *Env, id, name, mode string) []report.Finding {
	agent, f := sbxReadEnvironment(ctx, e, id, "read", "agent")
	if f != nil {
		return []report.Finding{*f}
	}
	if f := sbxValidateEnvironment(agent, name, mode); f != nil {
		return []report.Finding{*f}
	}
	current, f := sbxReadEnvironment(ctx, e, id, "current")
	if f != nil {
		return []report.Finding{*f}
	}
	if !maps.Equal(agent, current) {
		return []report.Finding{report.Failf("agent and runtime-launched process disagree on workspace or identity")}
	}
	return nil
}

func sbxEnvironmentProcess(ctx context.Context, e *Env, mode string) []report.Finding {
	id, cleanup, err := e.sandboxWith(ctx, []string{fixtureSbxEnvironment}, adapter.CreateOptions{WorkspaceMode: mode})
	if sbxWorkspaceUnavailable(err, mode) {
		return []report.Finding{report.Skipf("process observation without workspace unavailable: %v", err)}
	}
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer cleanup()
	initial, f := sbxReadEnvironment(ctx, e, id, "read", "agent")
	if f != nil {
		return []report.Finding{*f}
	}
	for _, value := range []string{"process-conflict", ""} {
		res, err := e.Adapter.ExecWith(ctx, id, adapter.ExecOptions{
			Cwd: "/tmp", Env: sbxConflictingEnvironment(value),
		}, sbxEnvironmentProbe, "process")
		if err != nil || res.ExitCode != 0 {
			return []report.Finding{report.Failf("exec with process environment and cwd: %v, exit %d: %s", err, res.ExitCode, res.Stderr)}
		}
		var observed struct {
			Environment map[string]string `json:"environment"`
			Cwd         string            `json:"cwd"`
		}
		if err := json.Unmarshal([]byte(res.Stdout), &observed); err != nil || observed.Cwd != "/tmp" {
			return []report.Finding{report.Failf("runtime-launched process did not run in its requested cwd /tmp")}
		}
		if !maps.Equal(observed.Environment, initial) {
			return []report.Finding{report.Failf("process overrides or cwd changed the assigned workspace or sandbox identity")}
		}
	}
	return nil
}

func sbxEnvironmentHooks(ctx context.Context, e *Env, mode string) []report.Finding {
	kits := []string{fixtureSbxEnvironment, "sbx-environment-hooks", "sbx-environment-exports"}
	if mode == "internal" {
		kits = append(kits, "sbx-workspace-reference")
	}
	id, cleanup, err := e.sandboxWith(ctx, kits, adapter.CreateOptions{WorkspaceMode: mode, Env: sbxConflictingEnvironment("runtime-conflict")})
	if err != nil {
		if sbxWorkspaceUnavailable(err, mode) {
			return []report.Finding{report.Skipf("no-workspace hook observation unavailable: %v", err)}
		}
		return []report.Finding{report.Failf("create with hooks: %v", err)}
	}
	defer cleanup()
	agent, f := sbxReadEnvironment(ctx, e, id, "read", "agent")
	if f != nil {
		return []report.Finding{*f}
	}
	if _, exists := agent["WORKSPACE_DIR"]; mode == "none" && exists {
		return []report.Finding{report.Failf("WORKSPACE_DIR present without an assigned workspace")}
	}
	for _, phase := range []string{"install", "startup"} {
		got, f := sbxReadEnvironment(ctx, e, id, "read", phase)
		if f != nil {
			return []report.Finding{*f}
		}
		if !maps.Equal(got, agent) {
			return []report.Finding{report.Failf("%s hook did not receive the agent's assigned environment", phase)}
		}
		got, f = sbxReadEnvironment(ctx, e, id, "read", phase+"-restricted")
		if f != nil {
			return []report.Finding{*f}
		}
		if len(got) != 0 {
			return []report.Finding{report.Failf("%s hook received unrequested sbx variables", phase)}
		}
	}
	expanded, f := sbxReadEnvironment(ctx, e, id, "expanded")
	if f != nil {
		return []report.Finding{*f}
	}
	expectedExpanded := maps.Clone(agent)
	if mode != "internal" {
		delete(expectedExpanded, "WORKSPACE_DIR")
	}
	if !maps.Equal(expanded, expectedExpanded) {
		return []report.Finding{report.Failf("create-time kit.env values disagree with the agent's environment")}
	}
	if mode == "none" {
		_, unexpectedCleanup, err := e.sandboxWith(ctx, append(kits, "sbx-workspace-reference"), adapter.CreateOptions{WorkspaceMode: mode})
		defer unexpectedCleanup()
		var refused *adapter.RefusedError
		if !errors.As(err, &refused) || !strings.Contains(err.Error(), "WORKSPACE_DIR") {
			return []report.Finding{report.Failf("a reference to absent WORKSPACE_DIR did not fail with a missing-name refusal")}
		}
	}
	return nil
}

func sbxIdentityStable(ctx context.Context, e *Env) []report.Finding {
	kits := []string{fixtureSbxEnvironment}
	if e.claims(capLifecycle) {
		kits = append(kits, "sbx-environment-hooks")
	}
	id, cleanup, err := e.sandbox(ctx, kits, nil)
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer cleanup()
	initial, f := sbxReadEnvironment(ctx, e, id, "read", "agent")
	if f != nil {
		return []report.Finding{*f}
	}
	// A changed host context must not cause persisted kit.env references to
	// be re-expanded on restart. Runtime identity still comes from the instance.
	savedEnv := e.Adapter.Env
	defer func() { e.Adapter.Env = savedEnv }()
	e.Adapter.Env = append(e.Adapter.Env, "SANDBOX_NAME="+HostSentinelValue+"-later")
	for _, operation := range []string{"restart", "replacement"} {
		// Clear previous captures: unchanged old files cannot prove that
		// a newly launched agent or hook received the expected values.
		if _, f := execOutput(ctx, e, id, sbxEnvironmentProbe, "clear"); f != nil {
			return []report.Finding{*f}
		}
		if operation == "restart" {
			if err := e.Adapter.Stop(ctx, id); err != nil {
				return []report.Finding{report.Failf("stop: %v", err)}
			}
			err = e.Adapter.Start(ctx, id)
		} else {
			if _, f := execOutput(ctx, e, id, sbxEnvironmentProbe, "scratch"); f != nil {
				return []report.Finding{*f}
			}
			err = e.Adapter.Recreate(ctx, id)
		}
		if err != nil {
			return []report.Finding{report.Failf("%s: %v", operation, err)}
		}
		if operation == "replacement" {
			if _, f := execOutput(ctx, e, id, sbxEnvironmentProbe, "fresh"); f != nil {
				return []report.Finding{report.Failf("replacement did not discard the writable layer")}
			}
		}
		got, f := sbxReadEnvironment(ctx, e, id, "read", "agent")
		if f != nil {
			return []report.Finding{*f}
		}
		if !maps.Equal(got, initial) {
			return []report.Finding{report.Failf("%s changed the sandbox's assigned identity or workspace", operation)}
		}
		if e.claims(capLifecycle) {
			for _, probe := range [][]string{{"read", "startup"}, {"expanded"}} {
				got, f := sbxReadEnvironment(ctx, e, id, probe...)
				if f != nil {
					return []report.Finding{*f}
				}
				want := maps.Clone(initial)
				if probe[0] == "expanded" {
					delete(want, "WORKSPACE_DIR")
				}
				if !maps.Equal(got, want) {
					return []report.Finding{report.Failf("%s changed hook identity or persisted expansion", operation)}
				}
			}
		}
	}
	return nil
}

func sbxIdentityNewAfterDeletion(ctx context.Context, e *Env) []report.Finding {
	opts := adapter.CreateOptions{Name: "kit-tck-identity-" + strings.ToLower(rand.Text())}
	var previous string
	for range 2 {
		id, cleanup, err := e.sandboxWith(ctx, []string{fixtureSbxEnvironment}, opts)
		if err != nil {
			return []report.Finding{report.Failf("create: %v", err)}
		}
		got, f := sbxReadEnvironment(ctx, e, id, "read", "agent")
		cleanup()
		if f != nil {
			return []report.Finding{*f}
		}
		if f := sbxValidateEnvironment(got, opts.Name, ""); f != nil {
			return []report.Finding{*f}
		}
		if got["SANDBOX_ID"] == previous {
			return []report.Finding{report.Failf("deletion and same-name creation reused the previous sandbox ID")}
		}
		previous = got["SANDBOX_ID"]
	}
	return nil
}
