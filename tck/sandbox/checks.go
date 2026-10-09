package sandbox

import (
	"context"
	"errors"
	"github.com/docker/sandbox-kit-spec/v3/spec"
	"path"
	"sort"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

// Capability types the checks are written against.
const (
	capLifecycle       = "com.docker.sandbox/lifecycle@1"
	capNetworkPolicy   = "com.docker.sandbox/network-policy@1"
	capNetworkPolicyV2 = "com.docker.sandbox/network-policy@2"
	capCredential      = "com.docker.sandbox/credential@1"
	capSbx             = "com.docker.sandbox/sbx@1"
)

// Fixture kits the suite composes. Each is a kit directory under
// testdata/, named by what it is for rather than by what it contains, so a
// check reads as the requirement it judges.
const (
	fixtureWorkload                    = "workload"
	fixtureSbxWorkload                 = "sbx-workload"
	fixtureHooks                       = "hooks"
	fixtureFiles                       = "files"
	fixtureScopedEgress                = "scoped-egress"
	fixtureCredentialInjectOnly        = "credential-inject-only"
	fixtureCredentialInjectOnlyPlain   = "credential-inject-only-plain"
	fixtureInjectOnlyControl           = "inject-only-control"
	fixtureInjectOnlyControlB          = "inject-only-control-b"
	fixtureCredentialInjectOnlyInstall = "credential-inject-only-install"
	// The same policy stated as @2, so the requirements @2 inherits are
	// judged against a kit declaring it rather than only against @1.
	fixtureScopedEgressV2           = "scoped-egress-v2"
	fixtureUnknownNeed              = "unknown-required-capability"
	fixtureRestrictedEnv            = "restricted-env"
	fixtureEgress                   = "egress"
	fixtureHTTPEgress               = "http-egress"
	fixtureCredential               = "credential"
	fixturePort                     = "port"
	fixtureUSBDevice                = "usb-device"
	fixtureAgentSessions            = "agent-sessions"
	fixtureAgentInteractiveSessions = "agent-interactive-sessions"
	fixtureKitRegistry              = "kit-registry"
)

// denyByDefault judges the connection-level allow list of whichever
// policy version the fixture states: egress it does not name is refused.
func denyByDefault(fixture string) func(context.Context, *Env) []report.Finding {
	return func(ctx context.Context, e *Env) []report.Finding {
		id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixture}, nil)
		if err != nil {
			return []report.Finding{report.Failf("create: %v", err)}
		}
		defer cleanup()

		allowed, err := e.Adapter.Exec(ctx, id, "kit-tck-probe", "https://example.com")
		if err != nil {
			return []report.Finding{report.Failf("probe allowed host: %v", err)}
		}
		if allowed.ExitCode != 0 {
			return []report.Finding{report.Failf("an allowed host was refused: %s", strings.TrimSpace(allowed.Stderr))}
		}
		denied, err := e.Adapter.Exec(ctx, id, "kit-tck-probe", "https://example.org")
		if err != nil {
			return []report.Finding{report.Failf("probe denied host: %v", err)}
		}
		switch denied.ExitCode {
		case 0:
			return []report.Finding{report.Failf("a host outside the allow list was reachable")}
		case 7:
			return nil
		default:
			// The probe reserves 7 for policy denial; anything else is
			// a transport failure, and counting it as denial would
			// pass a runtime with no policy over a broken network.
			return []report.Finding{report.Failf(
				"probing the denied host failed with status %d, which is not a policy observation: %s",
				denied.ExitCode, strings.TrimSpace(denied.Stderr))}
		}
	}
}

// installPhaseScoped judges the phase boundary of whichever policy
// version the fixture states: a domain opened for setup does not stay
// open for the agent.
func installPhaseScoped(fixture string) func(context.Context, *Env) []report.Finding {
	return func(ctx context.Context, e *Env) []report.Finding {
		id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixture}, nil)
		if err != nil {
			return []report.Finding{report.Failf("create: %v", err)}
		}
		defer cleanup()

		// The install hook recorded whether its own phase could reach
		// the install-only domain; the same probe now must not.
		during, f := execOutput(ctx, e, id, "cat", "/var/tmp/install-egress")
		during = strings.TrimSpace(during)
		if f != nil {
			return []report.Finding{*f}
		}
		if during != "reachable" {
			return []report.Finding{report.Failf(
				"install-only domain was %s during install; the install phase should have opened it", during)}
		}
		after, err := e.Adapter.Exec(ctx, id, "kit-tck-probe", "https://example.net")
		if err != nil {
			return []report.Finding{report.Failf("probe install-only host: %v", err)}
		}
		switch after.ExitCode {
		case 0:
			return []report.Finding{report.Failf(
				"an install-only domain is still reachable after the install phase closed")}
		case 7:
			return nil
		default:
			return []report.Finding{report.Failf(
				"probing the closed install-only host failed with status %d, which is not a policy observation: %s",
				after.ExitCode, strings.TrimSpace(after.Stderr))}
		}
	}
}

var checks = append(append(gitIdentityChecks, hostMountChecks...), []check{
	{requirement: "credential@1/sentinel-phase-scoped", capability: capCredential, needs: []string{capLifecycle}, run: credentialSentinelPhaseScoped},
	{requirement: "SPEC-v3 §6/env-expanded", capability: capLifecycle, run: finalEnvironmentPrecedence},
	{requirement: "ssh-agent@1/destination-keys-outside-sandbox", capability: capSSHAgent, run: sshAgentDestinationKeys},
	{requirement: "ssh-agent@1/agent-reachable", capability: capSSHAgent, run: sshAgentReachable},
	{requirement: "ssh-agent@1/operations-restricted", capability: capSSHAgent, run: sshAgentRestricted},
	{requirement: "ssh-agent@1/signatures-bounded", capability: capSSHAgent, run: sshAgentSignaturesBounded},
	{requirement: "ssh-agent@1/logins-bounded", capability: capSSHAgent, run: sshAgentLoginsBounded},
	{requirement: "ssh-agent@1/binding-verified", capability: capSSHAgent, run: sshAgentBindingVerified},
	{requirement: "ssh-agent@1/absent-without-grant", capability: capSSHAgent, run: sshAgentAbsentWithoutGrant},
	{requirement: "ssh-agent@1/phase-scoped", capability: capSSHAgent, needs: []string{capLifecycle}, run: sshAgentPhaseScoped},
	{requirement: "ssh-agent@1/every-boot", capability: capSSHAgent, run: sshAgentEveryBoot},
	{requirement: "ssh-agent@1/unavailable-refuses-required", capability: capSSHAgent, run: sshAgentRequiredRefused},
	{requirement: "ssh-agent@1/unavailable-skips-optional", capability: capSSHAgent, run: sshAgentOptionalSkipped},
	{requirement: "SPEC-v3 §7.1.1/validate-expanded-declarations", capability: capLifecycle, run: groupExpandedValidation},
	{requirement: "SPEC-v3 §7.1.1/atomic-selection", run: atomicSelection},
	{requirement: "SPEC-v3 §7.1.1/conflicts", capability: capLifecycle, run: groupConflicts},
	{requirement: "SPEC-v3 §7.1.1/lifetime", capability: capLifecycle, needs: []string{"com.docker.sandbox/volume@1"}, run: groupLifetime},
	{requirement: "SPEC-v3 §7.1.1/execution", capability: capLifecycle, run: groupExecution},

	{requirement: "long-running@1/survives-session-disconnect", capability: capLongRunning, run: survivesSessionDisconnect},
	{requirement: "long-running@1/explicit-stop-honored", capability: capLongRunning, run: longRunningStop},
	{requirement: "conformance.md §2.2/optional-long-running-accepted", run: optionalLongRunning},
	{
		// A required capability the runtime cannot provide has to be
		// refused: the author declared it because the kit does not work
		// without it, so proceeding would under-provision silently.
		requirement: "SPEC-v3 §7.3/unknown-required-refused",
		run: func(ctx context.Context, e *Env) []report.Finding {
			id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureUnknownNeed}, nil)
			defer cleanup()

			var refused *adapter.RefusedError
			switch {
			case errors.As(err, &refused):
				return nil
			case err != nil:
				return []report.Finding{report.Failf("create failed for a reason other than refusal: %v", err)}
			default:
				return []report.Finding{report.Failf(
					"a kit requiring an unimplemented capability was accepted as sandbox %s", id)}
			}
		},
	},
	{
		// The refusal checks below accept exit 2, so without this control
		// an adapter refusing every create would pass them while every
		// capability check skips — blanket refusal certified as
		// conformance. The workload alone requires nothing a claiming
		// runtime can lack, so this create has no legitimate refusal.
		requirement: "conformance.md §2.1/baseline-create-succeeds",
		run: func(ctx context.Context, e *Env) []report.Finding {
			id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload}, nil)
			if err != nil {
				return []report.Finding{report.Failf(
					"a workload-only sandbox must be creatable before any refusal means anything: %v", err)}
			}
			defer cleanup()
			_ = id
			return nil
		},
	},
	{
		// §2.2 applies to well-known types too, not only foreign ones: a
		// runtime that omits a type from `capabilities` and then quietly
		// accepts a kit requiring it would have that type's behavioral
		// checks skipped and pass overall while under-provisioning.
		requirement: "conformance.md §2.2/required-unclaimed-refused",
		run: func(ctx context.Context, e *Env) []report.Finding {
			var findings []report.Finding
			for _, probe := range []struct {
				capability, fixture string
				// The sbx fixture is a workload, not a mixin layered
				// onto one, so it is composed alone.
				alone bool
			}{
				{capability: capSbx, fixture: fixtureSbxWorkload, alone: true},
				{capability: capLongRunning, fixture: fixtureLongRunning},
				{capability: capGitIdentity, fixture: fixtureGitIdentity},
				{capLifecycle, fixtureHooks, false},
				{capNetworkPolicy, fixtureEgress, false},
				{capNetworkPolicyV2, fixtureHTTPEgress, false},
				{capCredential, fixtureCredential, false},
				{capSSHAgent, fixtureSSHAgent, false},
				{capAgentContext, fixtureContext, false},
				{capVolume, fixtureVolume, false},
				{capResources, fixtureResources, false},
				{capPrivileged, fixturePrivileged, false},
				{capAgentSkills, fixtureSkills, false},
				{capAgentSkill, fixtureBundledSkill, false},
				{capPort, fixturePort, false},
				{capUSBDevice, fixtureUSBDevice, false},
				{capAgentSessions, fixtureAgentSessions, false},
				{capAgentInteractiveSessions, fixtureAgentInteractiveSessions, false},
				{capKitRegistry, fixtureKitRegistry, false},
			} {
				if e.claims(probe.capability) {
					continue
				}
				compose := []string{fixtureWorkload, probe.fixture}
				if probe.alone {
					compose = []string{probe.fixture}
				}
				if probe.capability == capAgentSkill {
					// Without a supported destination, even an adapter
					// accepting the unclaimed skill can legitimately refuse.
					if !e.claims(capAgentSkills) {
						findings = append(findings, report.Skipf(
							"%s: refusal probe needs claimed %s", capAgentSkill, capAgentSkills))
						continue
					}
					compose = []string{fixtureBundledReader, probe.fixture}
				}
				id, cleanup, err := e.sandbox(ctx, compose, nil)
				var refused *adapter.RefusedError
				switch {
				case errors.As(err, &refused):
				case err != nil:
					findings = append(findings, report.Failf(
						"%s: create failed for a reason other than refusal: %v", probe.capability, err))
				default:
					cleanup()
					findings = append(findings, report.Failf(
						"a kit requiring unclaimed %s was accepted as sandbox %s", probe.capability, id))
				}
			}
			return findings
		},
	},
	{
		// Install runs once at create; startup runs on every boot. A
		// stop/start is the only way to tell them apart from outside.
		requirement: "lifecycle@1/install-once",
		capability:  capLifecycle,
		run: func(ctx context.Context, e *Env) []report.Finding {
			id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureHooks}, nil)
			if err != nil {
				return []report.Finding{report.Failf("create: %v", err)}
			}
			defer cleanup()

			before, f := execOutput(ctx, e, id, "cat", "/var/tmp/install.count")
			before = strings.TrimSpace(before)
			if f != nil {
				return []report.Finding{*f}
			}
			if before != "1" {
				return []report.Finding{report.Failf("install hook ran %s times at create, expected once", before)}
			}
			if err := e.Adapter.Stop(ctx, id); err != nil {
				return []report.Finding{report.Failf("stop: %v", err)}
			}
			if err := e.Adapter.Start(ctx, id); err != nil {
				return []report.Finding{report.Failf("start: %v", err)}
			}
			after, f := execOutput(ctx, e, id, "cat", "/var/tmp/install.count")
			after = strings.TrimSpace(after)
			if f != nil {
				return []report.Finding{*f}
			}
			if after != "1" {
				return []report.Finding{report.Failf("install hook ran again on reboot (count %s)", after)}
			}
			return nil
		},
	},
	{
		requirement: "lifecycle@1/startup-every-boot",
		capability:  capLifecycle,
		run: func(ctx context.Context, e *Env) []report.Finding {
			id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureHooks}, nil)
			if err != nil {
				return []report.Finding{report.Failf("create: %v", err)}
			}
			defer cleanup()

			// Each boot is observed on its own: an aggregate of 2 is also
			// what a runtime produces by running the hook twice at create
			// and never again.
			count, f := execOutput(ctx, e, id, "cat", "/var/tmp/startup.count")
			count = strings.TrimSpace(count)
			if f != nil {
				return []report.Finding{*f}
			}
			if count != "1" {
				return []report.Finding{report.Failf(
					"startup hook ran %s times during create, expected once", count)}
			}
			if err := e.Adapter.Stop(ctx, id); err != nil {
				return []report.Finding{report.Failf("stop: %v", err)}
			}
			if err := e.Adapter.Start(ctx, id); err != nil {
				return []report.Finding{report.Failf("start: %v", err)}
			}
			count, f = execOutput(ctx, e, id, "cat", "/var/tmp/startup.count")
			count = strings.TrimSpace(count)
			if f != nil {
				return []report.Finding{*f}
			}
			if count != "2" {
				return []report.Finding{report.Failf(
					"startup hook ran %s times across create and one reboot, expected twice", count)}
			}
			return nil
		},
	},
	{
		// A hook sees its declared env and nothing else: the host's
		// environment is not the kit's to inherit.
		requirement: "lifecycle@1/hook-env-restricted",
		capability:  capLifecycle,
		run: func(ctx context.Context, e *Env) []report.Finding {
			id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureRestrictedEnv}, nil)
			if err != nil {
				return []report.Finding{report.Failf("create: %v", err)}
			}
			defer cleanup()

			leaked, f := execOutput(ctx, e, id, "cat", "/var/tmp/hook.env")
			if f != nil {
				return []report.Finding{*f}
			}
			// A sentinel the suite put in the adapter's own environment:
			// the known VALUE is judged besides the names, because a leak
			// can also arrive through an allowed name.
			if strings.Contains(leaked, HostSentinelValue) {
				return []report.Finding{report.Failf(
					"a value from the host environment reached the hook, which sees only its declared env")}
			}
			// Names are judged exactly: a substring test accepts
			// UNDECLARED= for DECLARED=, and every leak that is not the
			// sentinel. What may appear is the declared name plus the
			// platform baseline the lifecycle@1 page defines — names a
			// shell needs or introduces itself, whose values must derive
			// from the image and sandbox; the sentinel scan above is what
			// catches a host value smuggled through an allowed name.
			baseline := map[string]bool{
				"PATH": true, "HOME": true, "HOSTNAME": true, "TERM": true,
				"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true,
			}
			var findings []report.Finding
			var declared bool
			for _, line := range strings.Split(leaked, "\n") {
				name, _, isAssignment := strings.Cut(strings.TrimSpace(line), "=")
				if !isAssignment || name == "" {
					// A multiline value's continuation, not a variable.
					continue
				}
				if name == "DECLARED" {
					declared = true
					continue
				}
				if !baseline[name] {
					findings = append(findings, report.Failf(
						"the hook's environment contains %q, which the kit never declared", name))
				}
			}
			if !declared {
				findings = append(findings, report.Failf(
					"the hook's declared env name is missing from its environment"))
			}
			return findings
		},
	},
	{
		// Files land before the agent starts, so the agent never observes
		// the sandbox without them.
		requirement: "lifecycle@1/files-written",
		capability:  capLifecycle,
		run: func(ctx context.Context, e *Env) []report.Finding {
			id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureFiles},
				map[string]string{"greeting": "hello"})
			if err != nil {
				return []report.Finding{report.Failf("create: %v", err)}
			}
			defer cleanup()

			body, f := execOutput(ctx, e, id, "cat", "/home/agent/.config/kit-written")
			if f != nil {
				return []report.Finding{*f}
			}
			if body != "hello" {
				return []report.Finding{report.Failf(
					"declared file holds %q; the arg reference should have expanded to %q", body, "hello")}
			}

			body, f = execOutput(ctx, e, id, "cat", "/home/agent/.config/env-written")
			if f != nil {
				return []report.Finding{*f}
			}
			if body != "hello" {
				return []report.Finding{report.Failf("final environment references did not expand in the declared file")}
			}

			// Whose it is, not only that it arrived: a declared file the
			// agent does not own is off the trust plane this capability
			// stays on, and its content reads the same either way. Both
			// answers come from exec, which runs as the agent.
			owner, f := execOutput(ctx, e, id, "stat", "-c", "%u", "/home/agent/.config/kit-written")
			if f != nil {
				return []report.Finding{*f}
			}
			agent, f := execOutput(ctx, e, id, "id", "-u")
			if f != nil {
				return []report.Finding{*f}
			}
			if strings.TrimSpace(owner) != strings.TrimSpace(agent) {
				return []report.Finding{report.Failf(
					"the declared file belongs to uid %s while the agent is uid %s; whichever user writes it, a file entry ends up the agent's — a path only root can own is an install hook's job",
					strings.TrimSpace(owner), strings.TrimSpace(agent))}
			}

			// Owning it is not the same as being able to change it, and
			// this entry declares no mode to explain a read-only one.
			res, err := e.Adapter.Exec(ctx, id, "test", "-w", "/home/agent/.config/kit-written")
			if err != nil {
				return []report.Finding{report.Failf("probe the declared file: %v", err)}
			}
			if res.ExitCode != 0 {
				return []report.Finding{report.Failf(
					"the declared file is not writable by the agent, and its entry declares no mode asking for that")}
			}
			return nil
		},
	},
	{
		// Deny-by-default is the whole point: an allow list that is not
		// exhaustive is not a policy.
		requirement: "network-policy@1/deny-by-default",
		capability:  capNetworkPolicy,
		run:         denyByDefault(fixtureScopedEgress),
	},
	{
		// @2 inherits every @1 requirement, and a runtime claiming only
		// @2 would otherwise be judged on none of them: the checks above
		// skip when @1 is unclaimed. The fixture states the same policy
		// as @2, so the host lists are judged whichever version a kit
		// declares.
		requirement: "network-policy@2/deny-by-default",
		capability:  capNetworkPolicyV2,
		run:         denyByDefault(fixtureScopedEgressV2),
	},
	{
		// Install-phase egress closes at the boundary: a domain opened for
		// setup must not stay open for the agent.
		requirement: "network-policy@1/install-phase-scoped",
		capability:  capNetworkPolicy,
		needs:       []string{capLifecycle},
		run:         installPhaseScoped(fixtureScopedEgress),
	},
	{
		requirement: "network-policy@2/install-phase-scoped",
		capability:  capNetworkPolicyV2,
		needs:       []string{capLifecycle},
		run:         installPhaseScoped(fixtureScopedEgressV2),
	},
	{
		// A host reachable at the connection level is still bounded by
		// the methods its HTTP rules name, or the rules grant nothing
		// the host list did not already grant.
		requirement: "network-policy@2/http-method-enforced",
		capability:  capNetworkPolicyV2,
		run: func(ctx context.Context, e *Env) []report.Finding {
			id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureHTTPEgress}, nil)
			if err != nil {
				return []report.Finding{report.Failf("create: %v", err)}
			}
			defer cleanup()

			if f := httpAllowed(ctx, e, id, "GET", "https://example.com/allowed/thing"); f != nil {
				return []report.Finding{*f}
			}
			// The same method against a host that allows it: this is what
			// attributes a 403 below to the boundary — if the origin
			// family answered POST itself with 403, this control fails
			// loudly instead of the refusal assertion passing falsely.
			if f := httpAllowed(ctx, e, id, "POST", "https://example.org/"); f != nil {
				return []report.Finding{*f}
			}
			return httpRefused(ctx, e, id, "POST", "https://example.com/allowed/thing",
				"a method outside the http allow rules was accepted")
		},
	},
	{
		// Same for the path: the rule names one prefix, and the rest of
		// the host is refused even for the allowed method.
		requirement: "network-policy@2/http-path-enforced",
		capability:  capNetworkPolicyV2,
		run: func(ctx context.Context, e *Env) []report.Finding {
			id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureHTTPEgress}, nil)
			if err != nil {
				return []report.Finding{report.Failf("create: %v", err)}
			}
			defer cleanup()

			if f := httpAllowed(ctx, e, id, "GET", "https://example.com/allowed/thing"); f != nil {
				return []report.Finding{*f}
			}
			return httpRefused(ctx, e, id, "GET", "https://example.com/elsewhere",
				"a path outside the http allow rules was accepted")
		},
	},
	{
		// Deny wins over allow, as it does for hosts. The denied request
		// matches an allow rule too, so only precedence refuses it — a
		// runtime that evaluated allow first would let it through.
		requirement: "network-policy@2/http-deny-precedence",
		capability:  capNetworkPolicyV2,
		run: func(ctx context.Context, e *Env) []report.Finding {
			id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureHTTPEgress}, nil)
			if err != nil {
				return []report.Finding{report.Failf("create: %v", err)}
			}
			defer cleanup()

			if f := httpAllowed(ctx, e, id, "GET", "https://example.com/allowed/thing"); f != nil {
				return []report.Finding{*f}
			}
			return httpRefused(ctx, e, id, "GET", "https://example.com/allowed/secret/thing",
				"a request matching both an allow and a deny rule was accepted")
		},
	},
	{
		// The sentinel is what the container sees; the real secret stays
		// on the host side of the proxy.
		requirement: "credential@1/secret-absent-in-sandbox",
		capability:  capCredential,
		run: func(ctx context.Context, e *Env) []report.Finding {
			id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureScopedEgress}, nil)
			if err != nil {
				return []report.Finding{report.Failf("create: %v", err)}
			}
			defer cleanup()

			// The suite bound a known secret for this service, so the
			// variable not being there is the runtime not injecting it.
			// Skipping here would let a runtime claim credential@1,
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
		},
	},
	{
		// An inject-only credential exists as outbound rewrites; the
		// judgment is a name-set diff against a baseline sandbox, so a
		// runtime inventing ANY variable for it is caught, not only one
		// spelled predictably. The fixture declares the contracted
		// service, so the suite's bound secret wires the credential —
		// an unbound entry could be skipped without exercising anything.
		requirement: "credential@1/inject-only-no-env",
		capability:  capCredential,
		run: func(ctx context.Context, e *Env) []report.Finding {
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
			for _, fixture := range []string{fixtureCredentialInjectOnly, fixtureCredentialInjectOnlyPlain, fixtureCredentialInjectOnlyInstall} {
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
			id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureCredentialInjectOnlyInstall}, nil)
			if err != nil {
				return []report.Finding{report.Failf("create %s: %v", fixtureCredentialInjectOnlyInstall, err)}
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
		},
	},
	{
		// The fixture's identity is uid 1234 named sbxagent, chosen
		// because it is not the conventional one: a runtime that hard-
		// codes the convention would pass against a conventional image
		// while reading nothing, and this is the composition that tells
		// the two apart.
		requirement: "sbx@1/honors-image-user",
		capability:  capSbx,
		run: func(ctx context.Context, e *Env) []report.Finding {
			id, cleanup, err := e.sandbox(ctx, []string{fixtureSbxWorkload}, nil)
			if err != nil {
				return []report.Finding{report.Failf("create: %v", err)}
			}
			defer cleanup()

			var findings []report.Finding
			// All four fields, because a runtime can read one and assume
			// the rest: the uid it execs as, the gid it owns writes with,
			// the login name commands run under, and the home they run
			// from.
			for _, want := range []struct {
				argv         []string
				expect, what string
			}{
				{[]string{"id", "-u"}, "1234", "uid"},
				{[]string{"id", "-g"}, "1234", "gid"},
				{[]string{"id", "-un"}, "sbxagent", "login name"},
				{[]string{"printenv", "HOME"}, "/home/sbxagent", "home"},
			} {
				got, f := execOutput(ctx, e, id, want.argv...)
				if f != nil {
					return append(findings, *f)
				}
				if strings.TrimSpace(got) != want.expect {
					findings = append(findings, report.Failf(
						"the image declares %s %s, but commands see %q; the identity is read from the image, not assumed",
						want.what, want.expect, strings.TrimSpace(got)))
				}
			}

			// The requirement covers hooks too, and a runtime can exec as
			// one identity while running its own hooks as another. Only
			// where the host claims lifecycle: without it there are no
			// hooks to observe. restricted-env is the fixture whose hook
			// records the environment it was given.
			if !e.claims(capLifecycle) {
				return findings
			}
			hookID, hookCleanup, err := e.sandbox(ctx, []string{fixtureSbxWorkload, fixtureRestrictedEnv}, nil)
			if err != nil {
				return append(findings, report.Failf("create with hooks: %v", err))
			}
			defer hookCleanup()

			hookEnv, f := execOutput(ctx, e, hookID, "cat", "/var/tmp/hook.env")
			if f != nil {
				return append(findings, *f)
			}
			if got := envVars(hookEnv)["HOME"]; got != "/home/sbxagent" {
				findings = append(findings, report.Failf(
					"the image declares home /home/sbxagent, but hooks ran with HOME %q; the identity is read from the image, not assumed", got))
			}
			return findings
		},
	},
	{
		// Where the profile lands is the observable form of "the host put
		// the workspace where the image said". The conventional fixture
		// cannot judge it: a runtime hard-coding /home/agent/workspace
		// passes there while ignoring what the image declares.
		requirement: "sbx@1/workspace-at-workdir",
		capability:  capSbx,
		// The profile is how the workspace's location is observed, and
		// resolution rightly skips an optional request the host does not
		// claim.
		needs: []string{capAgentContext},
		run: func(ctx context.Context, e *Env) []report.Finding {
			id, cleanup, err := e.sandbox(ctx, []string{fixtureSbxWorkload}, nil)
			if err != nil {
				return []report.Finding{report.Failf("create: %v", err)}
			}
			defer cleanup()

			// Beside the declared workdir, where agent-context@1 puts a
			// profile. The location is still derived from what the image
			// says: a runtime hard-coding the conventional identity
			// writes beside /home/agent/workspace instead, which is a
			// different path and no file here.
			const profile = "/home/sbxagent/AGENTS.md"
			res, err := e.Adapter.Exec(ctx, id, "cat", profile)
			if err != nil {
				return []report.Finding{report.Failf("read profile: %v", err)}
			}
			if res.ExitCode != 0 {
				return []report.Finding{report.Failf(
					"the image declares its working directory as /home/sbxagent/workspace, but nothing is at %s beside it; the workspace goes where the image says, not at a fixed path", profile)}
			}
			return nil
		},
	},
	{
		// The image's entrypoint is the agent's launch command, which the
		// host reads and runs itself. Left as PID 1 it would prepend
		// itself to whatever the host runs there, so the fixture's
		// entrypoint writes a marker only when it is init.
		requirement: "sbx@1/entrypoint-not-pid-one",
		capability:  capSbx,
		run: func(ctx context.Context, e *Env) []report.Finding {
			id, cleanup, err := e.sandbox(ctx, []string{fixtureSbxWorkload}, nil)
			if err != nil {
				return []report.Finding{report.Failf("create: %v", err)}
			}
			defer cleanup()

			res, err := e.Adapter.Exec(ctx, id, "cat", "/var/tmp/sbx-entrypoint-ran")
			if err != nil {
				return []report.Finding{report.Failf("probe entrypoint marker: %v", err)}
			}
			if res.ExitCode == 0 {
				return []report.Finding{report.Failf(
					"the image's entrypoint ran as PID 1; the host launches the agent itself and owns PID 1")}
			}
			return nil
		},
	},
}...)

// envVars parses `env` output into a name-to-value map.
func envVars(environ string) map[string]string {
	vars := map[string]string{}
	for _, line := range strings.Split(environ, "\n") {
		if name, value, ok := strings.Cut(line, "="); ok && name != "" {
			vars[name] = value
		}
	}
	return vars
}

// Capability types covered by the checks below.
const (
	capAgentContext             = "com.docker.sandbox/agent-context@1"
	capVolume                   = "com.docker.sandbox/volume@1"
	capResources                = "com.docker.sandbox/resources@1"
	capPrivileged               = "com.docker.sandbox/privileged@1"
	capAgentSkills              = "com.docker.sandbox/agent-skills@1"
	capPort                     = "com.docker.sandbox/port@1"
	capUSBDevice                = "com.docker.sandbox/usb-device@1"
	capAgentSessions            = "com.docker.sandbox/agent-sessions@1"
	capAgentInteractiveSessions = "com.docker.sandbox/agent-interactive-sessions@1"
	capKitRegistry              = "com.docker.sandbox/kit-registry@1"
)

const (
	fixtureContext        = "context"
	fixtureVolume         = "volume"
	fixtureResources      = "resources"
	fixturePrivileged     = "privileged"
	fixtureSkills         = "skills"
	fixtureSkillsRW       = "skills-writable"
	fixtureSkillsShared   = "skills-shared"
	fixtureSkillsOptional = "skills-optional"
)

// The skills checks all compose both mixins: one declaring read-only and
// one read-write, which is the arity the capability exists to support.
var (
	skillsComposition = []string{fixtureWorkload, fixtureSkills, fixtureSkillsRW}

	skillsReadOnlyPath = "/home/agent/.kit-tck/skills"
	skillsWritablePath = "/home/agent/.kit-tck/skills-rw"
)

func init() {
	checks = append(checks,
		check{
			// Two agents in one sandbox, as two agent mixins on a shell
			// compose. Both orders, so a runtime keeping only the first or
			// only the last explicit profile fails one of them. Every
			// profile must index all three staged bodies — both agents'
			// and the tool mixin's — or an agent misses a Kit.
			requirement: "agent-context@1/every-explicit-profile",
			capability:  capAgentContext,
			run: func(ctx context.Context, e *Env) []report.Finding {
				profiles := []string{"/home/agent/.codex/CLAUDE.md", "/home/agent/.kit-tck/second/AGENTS.md"}
				bodies := []string{
					path.Join(StagedKitRoot, fixtureContext, fixtureContext+".md"),
					path.Join(StagedKitRoot, "context-profile", "context-profile.txt"),
					path.Join(StagedKitRoot, "context-second-profile", "context-second-profile.txt"),
				}
				for _, kits := range [][]string{
					{fixtureWorkload, "context-profile", "context-second-profile", fixtureContext},
					{"context-second-profile", fixtureWorkload, "context-profile", fixtureContext},
				} {
					id, cleanup, err := e.sandbox(ctx, kits, nil)
					if err != nil {
						return []report.Finding{report.Failf("differing explicit profiles describe several agents and must compose: %v", err)}
					}
					defer cleanup()
					for _, profilePath := range profiles {
						profile, f := execOutput(ctx, e, id, "cat", profilePath)
						if f != nil {
							return []report.Finding{report.Failf("composition %v declares a profile at %s, but it is not readable: %s", kits, profilePath, f.Detail)}
						}
						for _, staged := range bodies {
							if !strings.Contains(profile, staged) {
								return []report.Finding{report.Failf("composition %v: the profile at %s does not index the Kit context at %s", kits, profilePath, staged)}
							}
						}
					}
				}
				return nil
			},
		},
		check{
			requirement: "agent-context@1/directory-honored",
			capability:  capAgentContext,
			run: func(ctx context.Context, e *Env) []report.Finding {
				// Two destinations catch both ignoring directory and hard-coding
				// the conventional global directory instead of reading config.
				for _, directory := range []string{"/home/agent/.codex", "/home/agent/.kit-tck/context"} {
					for _, kits := range [][]string{{"context-workload", fixtureContext}, {fixtureWorkload, "context-profile", fixtureContext}, {"context-profile", fixtureWorkload, fixtureContext}} {
						id, cleanup, err := e.sandbox(ctx, kits, map[string]string{"directory": directory})
						if err != nil {
							return []report.Finding{report.Failf("create with context directory %s: %v", directory, err)}
						}
						defer cleanup()
						filename := "CLAUDE.md"
						if kits[0] == "context-workload" {
							filename = "AGENTS.md"
						}
						profilePath := path.Join(directory, filename)
						profile, f := execOutput(ctx, e, id, "cat", profilePath)
						if f != nil {
							return []report.Finding{report.Failf("the composition declares its context profile at %s, but it is not readable: %s", profilePath, f.Detail)}
						}
						staged := path.Join(StagedKitRoot, fixtureContext, fixtureContext+".md")
						if !strings.Contains(profile, staged) {
							return []report.Finding{report.Failf("the profile at %s does not surface the mixin context at %s", profilePath, staged)}
						}
						if kits[0] == "context-workload" && !strings.Contains(profile, "kit-tck-existing-instructions") {
							return []report.Finding{report.Failf("the profile at %s lost its existing instructions", profilePath)}
						}
					}
				}
				return nil
			},
		},
		check{
			// Staging is what makes a kit self-describing: the body has to
			// be readable at the path the published descriptor names, with
			// no reference back to the build context.
			requirement: "agent-context@1/body-readable",
			capability:  capAgentContext,
			run: func(ctx context.Context, e *Env) []report.Finding {
				id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureContext}, nil)
				if err != nil {
					return []report.Finding{report.Failf("create: %v", err)}
				}
				defer cleanup()

				// The staged file being present is the artifact's doing,
				// which the kit suite already judges. What this capability
				// requires of a RUNTIME is surfacing each contributing
				// kit into the workload's profile, so that is what is
				// read: a runtime ignoring the capability entirely leaves
				// the profile without any reference to the mixin.
				profile, f := execOutput(ctx, e, id, "cat", ProfilePath)
				if f != nil {
					return []report.Finding{report.Failf(
						"the workload declares a profile file at %s, which the runtime did not write", ProfilePath)}
				}
				// The staged path is unique to this kit; the kit's NAME
				// appears in prose a runtime might copy for other
				// reasons, so matching it would pass on a coincidence.
				staged := path.Join(StagedKitRoot, fixtureContext, fixtureContext+".md")
				if !strings.Contains(profile, staged) {
					return []report.Finding{report.Failf(
						"the profile at %s does not reference %s, so the %s kit's context is not surfaced",
						ProfilePath, staged, fixtureContext)}
				}
				// The reference is only useful if the body behind it is
				// readable in the composed sandbox: a runtime could emit
				// the index entry while dropping the mixin's layers.
				body, f := execOutput(ctx, e, id, "cat", staged)
				if f != nil {
					return []report.Finding{report.Failf(
						"the profile references %s but the staged body is not readable: %s", staged, f.Detail)}
				}
				if body != ContextBody {
					return []report.Finding{report.Failf(
						"the staged body at %s reads %q, not the fixture's content", staged, body)}
				}
				return nil
			},
		},
		check{
			// Stop/start preserves the whole filesystem, so surviving it
			// proves nothing about the volume: only replacing the
			// container tells declared state from a directory in the
			// writable layer. The control marker is what makes the
			// observation valid — if it survives too, the layer was never
			// discarded and the volume's survival is unattributable.
			requirement: "volume@1/persists-across-recreate",
			capability:  capVolume,
			run: func(ctx context.Context, e *Env) []report.Finding {
				id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureVolume}, nil)
				if err != nil {
					return []report.Finding{report.Failf("create: %v", err)}
				}
				defer cleanup()

				if _, f := execOutput(ctx, e, id, "kit-tck-write", "/data/marker", "persisted"); f != nil {
					return []report.Finding{*f}
				}
				if _, f := execOutput(ctx, e, id, "kit-tck-write", "/var/tmp/kit-tck-scratch", "ephemeral"); f != nil {
					return []report.Finding{*f}
				}
				// Restart persistence first, on its own: recreate
				// preserving the volume says nothing about stop/start,
				// where an adapter could detach or clear it.
				if err := e.Adapter.Stop(ctx, id); err != nil {
					return []report.Finding{report.Failf("stop: %v", err)}
				}
				if err := e.Adapter.Start(ctx, id); err != nil {
					return []report.Finding{report.Failf("start: %v", err)}
				}
				restarted, f := execOutput(ctx, e, id, "cat", "/data/marker")
				if f != nil {
					return []report.Finding{*f}
				}
				if restarted != "persisted" {
					return []report.Finding{report.Failf("declared volume lost its contents across a restart")}
				}
				if err := e.Adapter.Recreate(ctx, id); err != nil {
					return []report.Finding{report.Failf("recreate: %v", err)}
				}

				scratch, err := e.Adapter.Exec(ctx, id, "cat", "/var/tmp/kit-tck-scratch")
				if err != nil {
					return []report.Finding{report.Failf("probe scratch: %v", err)}
				}
				if scratch.ExitCode == 0 {
					return []report.Finding{report.Failf(
						"the writable layer survived recreate, so nothing this check observes is attributable to the volume")}
				}
				body, f := execOutput(ctx, e, id, "cat", "/data/marker")
				if f != nil {
					return []report.Finding{*f}
				}
				if body != "persisted" {
					return []report.Finding{report.Failf("declared volume lost its contents across recreate")}
				}
				return nil
			},
		},
		check{
			// A declared limit the kernel does not enforce is a comment.
			requirement: "resources@1/limit-applied",
			capability:  capResources,
			run: func(ctx context.Context, e *Env) []report.Finding {
				id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixtureResources}, nil)
				var refused *adapter.RefusedError
				if errors.As(err, &refused) {
					// The page permits refusing an unsatisfiable request
					// over silently truncating it, so declining the 2g
					// fixture is conforming, not a failure.
					return []report.Finding{report.Skipf(
						"runtime refused the resource request: %s", refused.Detail)}
				}
				if err != nil {
					return []report.Finding{report.Failf("create: %v", err)}
				}
				defer cleanup()

				limit, f := execOutput(ctx, e, id, "kit-tck-memory-limit")
				limit = strings.TrimSpace(limit)
				if f != nil {
					return []report.Finding{*f}
				}
				// The fixture declares memory: 2g, so only that exact
				// enforced value counts as the declaration applied.
				// resources@1 says SHOULD and permits operator overrides,
				// so any other observation — no limit, an unreadable
				// cgroup, or some different finite value — is reported
				// rather than failed, but never silently counted as
				// conforming: cgroup v1 spells "unlimited" as a very large
				// finite number, which accepting any finite value would
				// mistake for enforcement.
				const declaredMemoryLimit = "2147483648"
				if limit != declaredMemoryLimit {
					return []report.Finding{report.Warnf(
						"the kit declared a %s-byte memory limit; the sandbox reports %q",
						declaredMemoryLimit, limit)}
				}
				return nil
			},
		},
		check{
			// Privilege is granted only where the kit asked for it, so the
			// grant has to be observable when it did.
			requirement: "privileged@1/elevation-granted",
			capability:  capPrivileged,
			run: func(ctx context.Context, e *Env) []report.Finding {
				id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, fixturePrivileged}, nil)
				defer cleanup()
				var refused *adapter.RefusedError
				if errors.As(err, &refused) {
					// The page says a host MAY refuse, and SHOULD require
					// explicit consent, so declining is conforming.
					return []report.Finding{report.Skipf("runtime refused the privilege request: %s", refused.Detail)}
				}
				if err != nil {
					return []report.Finding{report.Failf("create: %v", err)}
				}

				got, f := execOutput(ctx, e, id, "kit-tck-privileged")
				got = strings.TrimSpace(got)
				if f != nil {
					return []report.Finding{*f}
				}
				if got != "yes" {
					return []report.Finding{report.Failf(
						"the kit requested privilege; the sandbox reports %q", got)}
				}
				return nil
			},
		},
		check{
			// The point of the capability: the store reaches the path the
			// kit named, without the runtime having to recognize the
			// agent. A runtime keying off its own table of known agents
			// fails here, which is the hole this exists to close.
			//
			// Both mixins are composed, because the capability is
			// instance-shaped for exactly this case: a runtime that mounts
			// the first declaration and stops is the other way to fail it.
			requirement: "agent-skills@1/store-mounted-at-declared-path",
			capability:  capAgentSkills,
			run: func(ctx context.Context, e *Env) []report.Finding {
				id, cleanup, err := e.sandbox(ctx, skillsComposition, nil)
				if err != nil {
					return []report.Finding{report.Failf("create: %v", err)}
				}
				defer cleanup()

				var findings []report.Finding
				for _, path := range []string{skillsReadOnlyPath, skillsWritablePath} {
					findings = append(findings, storeMountedAt(ctx, e, id, path)...)
				}
				return findings
			},
		},
		check{
			// The kit asked for read-only and the adapter offers
			// read-write, so anything writable here is the runtime taking
			// the wider of the two instead of the narrower.
			requirement: "agent-skills@1/readonly-default-honored",
			capability:  capAgentSkills,
			run: func(ctx context.Context, e *Env) []report.Finding {
				id, cleanup, err := e.sandbox(ctx, skillsComposition, nil)
				if err != nil {
					return []report.Finding{report.Failf("create: %v", err)}
				}
				defer cleanup()

				res, err := e.Adapter.Exec(ctx, id,
					"kit-tck-write", skillsReadOnlyPath+"/intruder", "written")
				if err != nil {
					return []report.Finding{report.Failf("probe write: %v", err)}
				}
				if res.ExitCode == 0 {
					return []report.Finding{report.Failf(
						"the kit declared no mode, which means read-only, but the mount accepted a write")}
				}
				return nil
			},
		},
		check{
			// The page requires the mount before lifecycle hooks run, so
			// an install hook can read what the user shared. The skills
			// fixture's install hook records what it saw; probing only
			// after create would let a runtime mount late and still pass.
			requirement: "agent-skills@1/mounted-before-hooks",
			capability:  capAgentSkills,
			needs:       []string{capLifecycle},
			run: func(ctx context.Context, e *Env) []report.Finding {
				id, cleanup, err := e.sandbox(ctx, skillsComposition, nil)
				if err != nil {
					return []report.Finding{report.Failf("create: %v", err)}
				}
				defer cleanup()

				// Every declared path is judged, or a runtime could mount
				// the first declaration on time and the rest after hooks.
				var findings []report.Finding
				for _, recorded := range []struct{ path, record string }{
					{skillsReadOnlyPath, "/var/tmp/skills-at-install"},
					{skillsWritablePath, "/var/tmp/skills-rw-at-install"},
				} {
					seen, f := execOutput(ctx, e, id, "cat", recorded.record)
					if f != nil {
						findings = append(findings, *f)
						continue
					}
					if !listsExactly(seen, SkillName) {
						findings = append(findings, report.Failf(
							"the install hook did not see %s at %s, so that mount arrived after hooks ran: %q",
							SkillName, recorded.path, strings.TrimSpace(seen)))
					}
				}
				return findings
			},
		},
		check{
			// Two kits naming one path with different modes resolve to the
			// widest declared mode: one mount cannot be both, and the
			// narrower declaration was never an isolation boundary inside
			// a shared filesystem. The widening is on the surface, so it
			// is approved, not silent.
			requirement: "agent-skills@1/shared-path-widest-mode",
			capability:  capAgentSkills,
			run: func(ctx context.Context, e *Env) []report.Finding {
				id, cleanup, err := e.sandbox(ctx,
					[]string{fixtureWorkload, fixtureSkills, fixtureSkillsShared}, nil)
				if err != nil {
					return []report.Finding{report.Failf("create: %v", err)}
				}
				defer cleanup()

				if f := storeMountedAt(ctx, e, id, skillsReadOnlyPath); f != nil {
					return f
				}
				res, err := e.Adapter.Exec(ctx, id,
					"kit-tck-write", skillsReadOnlyPath+"/merged", "written")
				if err != nil {
					return []report.Finding{report.Failf("probe write: %v", err)}
				}
				if res.ExitCode != 0 {
					return []report.Finding{report.Failf(
						"a co-composed kit declared this path readwrite, but the merged mount refused a write: %s",
						strings.TrimSpace(res.Stderr))}
				}
				return nil
			},
		},
		check{
			// The other half of the two-sided bound: the host narrows.
			// Every other skills check runs against the contract's
			// most-permissive default, which can only show the kit's half.
			requirement: "agent-skills@1/host-readonly-narrows",
			capability:  capAgentSkills,
			run: func(ctx context.Context, e *Env) []report.Finding {
				id, cleanup, err := e.sandboxWith(ctx, skillsComposition,
					adapter.CreateOptions{SkillsHostMode: "readonly"})
				if err != nil {
					return []report.Finding{report.Failf("create: %v", err)}
				}
				defer cleanup()

				// The mount itself must still arrive: readonly withholds
				// write, not the store.
				if f := storeMountedAt(ctx, e, id, skillsWritablePath); f != nil {
					return f
				}
				res, err := e.Adapter.Exec(ctx, id,
					"kit-tck-write", skillsWritablePath+"/intruder", "written")
				if err != nil {
					return []report.Finding{report.Failf("probe write: %v", err)}
				}
				if res.ExitCode == 0 {
					return []report.Finding{report.Failf(
						"the host offers read-only, but the kit's readwrite request was granted anyway")}
				}
				return nil
			},
		},
		check{
			// Host content is supplemental, even for a required destination.
			requirement: "agent-skills@1/host-store-optional",
			capability:  capAgentSkills,
			run:         skillsWithoutHost(fixtureSkills, skillsReadOnlyPath, adapter.CreateOptions{SkillsHostMode: "off"}),
		},
		check{
			requirement: "agent-skills@1/host-off-keeps-optional",
			capability:  capAgentSkills,
			run:         skillsWithoutHost(fixtureSkillsOptional, "/home/agent/.kit-tck/skills-opt", adapter.CreateOptions{SkillsHostMode: "off"}),
		},
		check{
			// "Every declared path receives the same store" means one
			// store, not one copy per path: independent copies satisfy
			// every listing, so only a write observed through a different
			// declared path tells them apart.
			requirement: "agent-skills@1/same-store-at-every-path",
			capability:  capAgentSkills,
			run: func(ctx context.Context, e *Env) []report.Finding {
				id, cleanup, err := e.sandbox(ctx, skillsComposition, nil)
				if err != nil {
					return []report.Finding{report.Failf("create: %v", err)}
				}
				defer cleanup()

				const probe = "kit-tck-cross-path-probe"
				res, err := e.Adapter.Exec(ctx, id,
					"kit-tck-write", skillsWritablePath+"/"+probe, "written")
				if err != nil {
					return []report.Finding{report.Failf("probe write: %v", err)}
				}
				if res.ExitCode != 0 {
					return []report.Finding{report.Failf(
						"the writable mount refused the probe write: %s", strings.TrimSpace(res.Stderr))}
				}

				listing, f := execOutput(ctx, e, id, "ls", skillsReadOnlyPath)
				if f != nil {
					return []report.Finding{*f}
				}
				if !listsExactly(listing, probe) {
					return []report.Finding{report.Failf(
						"a write through %s is invisible through %s, so the declared paths are backed by separate copies rather than the one shared store",
						skillsWritablePath, skillsReadOnlyPath)}
				}
				return nil
			},
		},
		check{
			// And the narrower rule is not just "always read-only": a kit
			// that asks for write, on a host offering it, gets it.
			requirement: "agent-skills@1/readwrite-granted-when-both-allow",
			capability:  capAgentSkills,
			run: func(ctx context.Context, e *Env) []report.Finding {
				id, cleanup, err := e.sandbox(ctx, skillsComposition, nil)
				if err != nil {
					return []report.Finding{report.Failf("create: %v", err)}
				}
				defer cleanup()

				// What must be writable is the shared store, so the store
				// has to be what is mounted here: an unrelated scratch
				// directory would accept the write and mean nothing.
				if f := storeMountedAt(ctx, e, id, skillsWritablePath); f != nil {
					return f
				}

				res, err := e.Adapter.Exec(ctx, id,
					"kit-tck-write", skillsWritablePath+"/added", "written")
				if err != nil {
					return []report.Finding{report.Failf("probe write: %v", err)}
				}
				if res.ExitCode != 0 {
					return []report.Finding{report.Failf(
						"both sides allow write, but the mount refused it: %s", strings.TrimSpace(res.Stderr))}
				}
				return nil
			},
		},
	)
}

// storeMountedAt reports why path is not the host's shared skills store,
// or nil when it is. The marker the adapter seeded is what tells the
// store apart from an empty directory or an unrelated one.
func storeMountedAt(ctx context.Context, e *Env, id, path string) []report.Finding {
	listing, f := execOutput(ctx, e, id, "ls", path)
	if f != nil {
		return []report.Finding{report.Failf(
			"the kit declared skills at %s and the runtime did not mount it: %s", path, f.Detail)}
	}
	// Matched as a whole entry: a substring would accept a neighbouring
	// name such as "<marker>-old".
	if !listsExactly(listing, SkillName) {
		return []report.Finding{report.Failf(
			"%s is mounted but does not hold %s, so it is not the shared store", path, SkillName)}
	}
	return nil
}

// listsExactly reports whether an ls listing contains name as a whole
// entry.
func listsExactly(listing, name string) bool {
	for _, line := range strings.Split(listing, "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

// Fixture argument exports are legitimate workload environment, not credential
// injection. Compare their values rather than exempting names from leak checks.
func fixtureEnvironmentExports(name string) (map[string]string, error) {
	// Suite metadata belongs to the fixture source, not its runtime-facing
	// reference, which may have been uploaded to a registry by the resolver.
	raw, err := embeddedFixtures.ReadFile(path.Join(FixtureDir, name, name+".yaml"))
	if err != nil {
		return nil, err
	}
	descriptor, err := spec.Decode(raw)
	if err != nil {
		return nil, err
	}
	values, err := spec.KitArgValues(descriptor.Args, nil)
	if err != nil {
		return nil, err
	}
	exports := map[string]string{}
	for name, arg := range descriptor.Args {
		if arg.BuildArg == "" && arg.Env != "" {
			if value, ok := values[name]; ok {
				exports[arg.Env] = value
			}
		}
	}
	return exports, nil
}
