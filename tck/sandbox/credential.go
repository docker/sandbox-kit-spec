package sandbox

import (
	"context"
	"sort"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

func credentialSecretAbsent(fixture string) func(context.Context, *Env) []report.Finding {
	return func(ctx context.Context, e *Env) []report.Finding {
		id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixture}, nil)
		if err != nil {
			return []report.Finding{report.Failf("create: %v", err)}
		}
		defer cleanup()

		// The suite bound a known secret for this service, so the
		// variable not being there is the runtime not injecting it.
		// Skipping here would let a runtime claim credentials,
		// implement nothing, and pass its only credential check.
		value, f := execOutput(ctx, e, id, "printenv", "KIT_TCK_TOKEN")
		value = strings.TrimSpace(value)
		if f != nil {
			return []report.Finding{report.Failf(
				"the kit declares a credential and the suite bound one, but %s is not set in the sandbox", "KIT_TCK_TOKEN")}
		}
		if value == "" {
			return []report.Finding{report.Failf("declared credential produced no value in the container")}
		}
		// The bound secret is known, so leakage is detected rather
		// than guessed from how the value is spelled: a real token
		// need not look like one. Containment, not equality — a
		// decorated form such as "Bearer <secret>" still places the
		// secret in the container.
		if strings.Contains(value, e.Secret) {
			return []report.Finding{report.Failf(
				"the bound secret reached the container; a proxy-managed credential must present a sentinel")}
		}
		// And absent from the WHOLE environment: a sentinel in the
		// declared name proves nothing if the real secret sits under
		// some other variable.
		environ, f := execOutput(ctx, e, id, "env")
		if f != nil {
			return []report.Finding{*f}
		}
		if strings.Contains(environ, e.Secret) {
			return []report.Finding{report.Failf(
				"the bound secret is in the container environment under a name other than the declared one")}
		}
		return nil
	}
}

func credentialInjectOnlyNoEnv(suffix string) func(context.Context, *Env) []report.Finding {
	fixtures := []string{fixtureCredentialInjectOnly + suffix, fixtureCredentialInjectOnlyPlain + suffix, fixtureCredentialInjectOnlyInstall + suffix}
	installFixture := fixtureCredentialInjectOnlyInstall + suffix
	return func(ctx context.Context, e *Env) []report.Finding {
		// Two baselines composing two DIFFERENT control kits with the
		// same policy: a variable whose value agrees across both is
		// stable — per-sandbox values (ids, hostnames) and
		// runtime-owned values that vary with the composed kit set
		// (the controls' names differ) both self-exclude, so every
		// remaining stable variable can be judged by value without
		// false positives. The controls carry the fixtures' network
		// policy, so a proxy variable the policy introduces is
		// baseline, not a delta pinned on the credential.
		var baselines [2]map[string]string
		for j, control := range []string{fixtureInjectOnlyControl, fixtureInjectOnlyControlB} {
			baseID, baseCleanup, err := e.sandbox(ctx, []string{fixtureWorkload, control}, nil)
			if err != nil {
				return []report.Finding{report.Failf("create baseline: %v", err)}
			}
			defer baseCleanup()
			baseEnv, f := execOutput(ctx, e, baseID, "env")
			if f != nil {
				return []report.Finding{*f}
			}
			baselines[j] = envVars(baseEnv)
		}
		// Both management forms — the duty is not conditioned on
		// proxyManaged, and real kits (devin) declare the default —
		// and the install-phase fixture too: an install credential is
		// revoked before the entrypoint, so its sentinel or secret in
		// the post-create environment is presence like any other.
		for _, fixture := range fixtures {
			id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixture}, nil)
			if err != nil {
				return []report.Finding{report.Failf("create %s: %v", fixture, err)}
			}
			defer cleanup()
			composedEnv, f := execOutput(ctx, e, id, "env")
			if f != nil {
				return []report.Finding{*f}
			}
			composed := envVars(composedEnv)
			exports, err := fixtureEnvironmentExports(fixture)
			if err != nil {
				return []report.Finding{report.Failf("read fixture exports: %v", err)}
			}

			var added []string
			for name := range composed {
				_, inA := baselines[0][name]
				_, inB := baselines[1][name]
				value, exported := exports[name]
				if !inA && !inB && (!exported || value != composed[name]) {
					added = append(added, name)
				}
			}
			if len(added) > 0 {
				sort.Strings(added)
				return []report.Finding{report.Failf(
					"composing %s added environment variables %v; an inject-only credential must add none, sentinel or otherwise", fixture, added)}
			}
			// A sentinel smuggled into a variable the workload already
			// owns is environment presence too: every stable variable
			// keeps its credential-free value. Kit-set-varying names
			// already self-excluded via the differing controls.
			for name, value := range baselines[0] {
				other, present := baselines[1][name]
				if !present || other != value {
					continue // varies without the credential; not judged
				}
				if got, ok := composed[name]; ok && got != value {
					return []report.Finding{report.Failf(
						"composing %s changed %s from %q to %q; an inject-only credential must not repurpose existing variables", fixture, name, value, got)}
				}
			}
			// The credential is wired (the suite bound its service), so
			// the real secret exists — and must not surface in any
			// existing variable's value either.
			if strings.Contains(composedEnv, e.Secret) {
				return []report.Finding{report.Failf(
					"the bound secret of an inject-only credential is in the container environment (%s)", fixture)}
			}
		}

		// The install leg needs the fixture's optional lifecycle
		// hook; a runtime claiming credentials without lifecycle
		// composes no hook, and a missing capture file would read as
		// credential non-conformance. The steady-state judgment above
		// already ran.
		if !e.claims(capLifecycle) {
			return nil
		}
		// The duty is phase-blind: an install-phase inject-only
		// credential must not surface in install hooks either — a
		// runtime could expose a sentinel there, remove it before
		// startup, and pass every steady-state probe above. The
		// fixture records its hook environment; names are judged
		// exactly against the declared name plus the lifecycle
		// baseline, and values against the bound secret.
		// Two credential-free hook baselines (the restricted-env
		// fixture has the identical hook shape): a hook variable
		// whose value agrees across both is stable, so a sentinel
		// smuggled into an existing variable's VALUE is caught, and
		// per-sandbox values exclude themselves.
		var hookBaselines [2]map[string]string
		for j := range hookBaselines {
			baseID, baseCleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureRestrictedEnv}, nil)
			if err != nil {
				return []report.Finding{report.Failf("create hook baseline: %v", err)}
			}
			defer baseCleanup()
			baseEnv, f := execOutput(ctx, e, baseID, "cat", "/var/tmp/hook.env")
			if f != nil {
				return []report.Finding{*f}
			}
			hookBaselines[j] = envVars(baseEnv)
		}
		id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, installFixture}, nil)
		if err != nil {
			return []report.Finding{report.Failf("create %s: %v", installFixture, err)}
		}
		defer cleanup()
		hookEnv, f := execOutput(ctx, e, id, "cat", "/var/tmp/hook.env")
		if f != nil {
			return []report.Finding{*f}
		}
		if strings.Contains(hookEnv, e.Secret) {
			return []report.Finding{report.Failf(
				"the bound secret of an install-phase inject-only credential reached an install hook")}
		}
		allowed := map[string]bool{
			"PATH": true, "HOME": true, "HOSTNAME": true, "TERM": true,
			"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true,
			"DECLARED": true,
		}
		for name, got := range envVars(hookEnv) {
			if !allowed[name] {
				return []report.Finding{report.Failf(
					"an install-phase inject-only credential surfaced %q in a hook's environment; it must add no variable in any phase", name)}
			}
			// Value smuggling: a stable hook variable must keep its
			// credential-free value.
			value, present := hookBaselines[1][name]
			if base, ok := hookBaselines[0][name]; ok && present && base == value && got != value {
				return []report.Finding{report.Failf(
					"an install-phase inject-only credential changed hook variable %q from %q to %q; it must not repurpose existing variables", name, value, got)}
			}
		}
		return nil
	}
}
