package sandbox

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

// runAgainstFake drives the suite against a runtime that exists only to be
// driven. Without it the suite would ship never having run: a conformance
// harness that has only ever been read cannot be trusted to fail when it
// should.
func runAgainstFake(t *testing.T, broken string, selected ...check) report.Report {
	t.Helper()
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{
		"KIT_TCK_FAKE_STATE=" + t.TempDir(),
		"KIT_TCK_FAKE_BROKEN=" + broken,
		"KIT_TCK_FAKE_CLAIMS=",
	}

	if len(selected) == 0 {
		selected = checks
	}
	rep, err := runChecks(t.Context(), &Env{
		Adapter:  a,
		Fixtures: Fixtures(FixtureDir),
	}, selected)
	require.NoError(t, err, "the suite must run even when the runtime is wrong")
	return rep
}

func TestFakeSSHAgentMatchesFixtureNotParentPath(t *testing.T) {
	for _, claims := range []string{capSSHAgent, groupVolume} {
		t.Run(claims, func(t *testing.T) {
			a := adapter.New(filepath.Join("testdata", "fake-adapter"))
			a.Env = []string{
				"KIT_TCK_FAKE_STATE=" + t.TempDir(),
				"KIT_TCK_FAKE_CLAIMS=" + claims,
				"KIT_TCK_FAKE_BROKEN=",
			}
			kit := filepath.Join(t.TempDir(), "ssh-agent-checkout", "workload")
			id, err := a.Create(t.Context(), []string{kit}, adapter.CreateOptions{})
			require.NoError(t, err, "the checkout name must not require an SSH agent for a plain workload")
			require.NotEmpty(t, id)
		})
	}
}

// The reason unclaimed capabilities skip at all: a runtime implementing
// one capability well is conforming for that claim. Its checks must run
// and pass while everything else skips — not fail because a fixture
// dragged in a required capability the runtime rightly refused.
func TestASingleCapabilityRuntimeIsJudgedOnlyOnItsClaim(t *testing.T) {
	// Host controls must not turn the conforming fake into a broken one.
	t.Setenv("KIT_TCK_FAKE_BROKEN", "refuses-everything")

	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{
		"KIT_TCK_FAKE_STATE=" + t.TempDir(),
		"KIT_TCK_FAKE_CLAIMS=com.docker.sandbox/volume@1",
		"KIT_TCK_FAKE_BROKEN=",
	}

	rep, err := Run(context.Background(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)})
	require.NoError(t, err)
	require.False(t, rep.Failed(), "a partial implementation is conforming for what it claims:\n%s", rep)

	for _, f := range rep.Findings {
		if f.Requirement == "volume@1/mounted-before-hooks" {
			require.Equal(t, report.Skip, f.Severity, "hook observations require lifecycle support")
			continue
		}
		require.NotContains(t, f.Requirement, "volume@1",
			"the claimed capability's checks must run clean, not skip: %s", f)
	}
}

func TestAtomicSelectionForPartialRuntimes(t *testing.T) {
	for _, tc := range []struct {
		claim  string
		broken string
	}{
		{capLifecycle, ""},
		{groupVolume, ""},
		{capLifecycle, "ordinary-ignore-optional-rejection"},
		{capLifecycle, "ordinary-ignore-required-rejection"},
		{capLifecycle, "partial-group-applies-lifecycle"},
		{capLifecycle, "partial-required-skips-volume"},
		{groupVolume, "partial-required-skips-lifecycle"},
		{groupVolume, "partial-group-applies-volume"},
	} {
		t.Run(tc.claim+"/"+tc.broken, func(t *testing.T) {
			a := adapter.New(filepath.Join("testdata", "fake-adapter"))
			a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + tc.claim, "KIT_TCK_FAKE_BROKEN=" + tc.broken}
			rep, err := Run(t.Context(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)})
			require.NoError(t, err)
			if tc.broken != "" {
				require.Contains(t, failedRequirements(rep), "SPEC-v3 §7.1.1/atomic-selection")
				return
			}
			require.False(t, rep.Failed(), "%s", rep)
			for _, finding := range rep.Findings {
				if finding.Requirement == "SPEC-v3 §7.1.1/atomic-selection" {
					require.NotEqual(t, report.Skip, finding.Severity)
				}
			}
		})
	}
}

func TestLongRunningNeedsNoHelperCapabilities(t *testing.T) {
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{
		"KIT_TCK_FAKE_STATE=" + t.TempDir(),
		"KIT_TCK_FAKE_CLAIMS=" + capLongRunning,
		"KIT_TCK_FAKE_BROKEN=",
	}
	rep, err := Run(context.Background(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)})
	require.NoError(t, err)
	require.False(t, rep.Failed(), "long-running alone must be testable:\n%s", rep)
	for _, f := range rep.Findings {
		require.NotContains(t, f.Requirement, "long-running",
			"long-running checks must run clean, not skip: %s", f)
	}
}

// The refusal duty covers well-known types too: a runtime omitting a type
// from its claims and then accepting a kit that requires it would have
// that type's checks skipped and pass while under-provisioning.
func TestARequiredUnclaimedTypeMustBeRefused(t *testing.T) {
	t.Parallel()
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{
		"KIT_TCK_FAKE_STATE=" + t.TempDir(),
		"KIT_TCK_FAKE_CLAIMS=com.docker.sandbox/volume@1",
		"KIT_TCK_FAKE_BROKEN=accepts-required-unclaimed",
	}

	rep, err := Run(context.Background(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)})
	require.NoError(t, err)
	require.Contains(t, failedRequirements(rep), "conformance.md §2.2/required-unclaimed-refused",
		"accepting a kit that requires an unclaimed well-known type must fail:\n%s", rep)
}

// failedRequirements names what the run judged non-conforming.
func failedRequirements(rep report.Report) []string {
	var out []string
	for _, f := range rep.Findings {
		if f.Severity == report.Fail {
			out = append(out, f.Requirement)
		}
	}
	return out
}

func TestAConformingRuntimePasses(t *testing.T) {
	// An empty mutation must override a broken mode inherited from the host.
	t.Setenv("KIT_TCK_FAKE_BROKEN", "refuses-everything")

	rep := runAgainstFake(t, "")
	require.False(t, rep.Failed(), "conforming fake reported failures:\n%s", rep)
}

func TestBackingAgentCloseWithIdleClient(t *testing.T) {
	a, err := startBackingAgent()
	require.NoError(t, err)
	conn, err := net.Dial("unix", a.Socket())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	// The server must have accepted this client before Close, otherwise
	// the test would only exercise closing a listener with no handlers.
	require.Eventually(t, func() bool {
		a.connsMu.Lock()
		defer a.connsMu.Unlock()
		return len(a.conns) == 1
	}, time.Second, time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- a.Close() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("closing the backing agent blocked on an idle client")
	}
}

// Each of these breaks one behavior, so a check that never fails would be
// caught here rather than by trusting it.
// mutations maps a way of breaking the fake runtime to the requirements
// that must notice. One behavior can be named by more than one
// requirement — the two network-policy versions state the same duty about
// the host lists — and dropping it has to fail every one of them.
var mutations = map[string][]string{
	"volume-keys-on-wrapper":            {"volume@1/composition-independent"},
	"volume-refusal-destroys-storage":   {"volume@1/recreate-config-compatible"},
	"volume-refuses-matching":           {"volume@1/matching-requests-merge"},
	"volume-merges-conflicts":           {"volume@1/no-silent-merge"},
	"volume-shares-instances":           {"volume@1/instance-and-path-identity"},
	"volume-ignores-path":               {"volume@1/instance-and-path-identity"},
	"volume-keys-on-kit":                {"volume@1/composition-independent"},
	"volume-keeps-writable-layer":       {"volume@1/composition-independent"},
	"volume-resets-mode":                {"volume@1/composition-independent"},
	"volume-forgets-undeclared":         {"volume@1/undeclared-retained"},
	"volume-mounts-undeclared":          {"volume@1/undeclared-retained"},
	"volume-retains-after-removal":      {"volume@1/removal-deletes-storage"},
	"volume-orphans-after-removal":      {"volume@1/removal-deletes-storage"},
	"volume-reuses-name":                {"volume@1/removal-deletes-storage"},
	"volume-ignores-recreate-config":    {"volume@1/recreate-config-compatible"},
	"volume-refusal-destroys-container": {"volume@1/recreate-config-compatible"},
	"volume-copies-image":               {"volume@1/initially-empty"},
	"volume-tmpfs-persists-stop":        {"volume@1/tmpfs-cleared"},
	"volume-tmpfs-persists-recreate":    {"volume@1/tmpfs-cleared"},
	"volume-after-hooks":                {"volume@1/mounted-before-hooks"},
	"volume-after-restart-hooks":        {"volume@1/mounted-before-hooks"},
	"volume-after-recreate-hooks":       {"volume@1/mounted-before-hooks"},
	"host-mount-after-hooks":            {"host-mount@1/mounted-before-hooks"},
	"host-mount-per-sandbox":            {"host-mount@1/shared-concurrently"},
	"host-mount-lost-on-removal":        {"host-mount@1/survives-sandbox-removal"},
	"host-mount-uses-display-identity":  {"host-mount@1/isolated-by-kit"},
	"host-mount-ignores-path":           {"host-mount@1/isolated-by-path"},
	"host-mount-keys-full-reference":    {"host-mount@1/survives-kit-update"},
	"host-mount-not-listed":             {"host-mount@1/listed-and-removable"},
	"host-mount-not-host-visible":       {"host-mount@1/listed-and-removable"},
	"host-mount-remove-noop":            {"host-mount@1/listed-and-removable"},
	"host-mount-removes-metadata-only":  {"host-mount@1/listed-and-removable"},
	"host-mount-removes-other-kit":      {"host-mount@1/listed-and-removable"},
	"host-mount-root-unwritable":        {"host-mount@1/agent-writable-root"},
	"host-mount-ignores-mode":           {"host-mount@1/initial-mode-applied"},
	"host-mount-resets-mode":            {"host-mount@1/initial-mode-applied"},
	"host-mount-merges-conflicts":       {"host-mount@1/no-silent-merge", "host-mount@1/host-volume-conflict"},
	"host-mount-grant-as-volume":        {"host-mount@1/separate-permission-surface"},
	"host-mount-accepts-unclaimed":      {"host-mount@1/unadvertised-is-unmet"},
	"host-mount-refuses-optional":       {"host-mount@1/unadvertised-is-unmet"},
	"host-mount-unclaimed-grant":        {"host-mount@1/unadvertised-is-unmet"},
	"host-mount-omits-skip":             {"host-mount@1/unadvertised-is-unmet"},
	"bundle-drops-register":             {"agent-skill@1/exposed", "agent-skill@1/before-launch"},
	"bundle-corrupts-register":          {"agent-skill@1/exposed"},
	"bundle-register-not-executable":    {"agent-skill@1/exposed"},
	"bundle-overwrites-host":            {"agent-skill@1/host-conflict"},
	"bundle-ignores":                    {"agent-skill@1/exposed"},
	"bundle-first-only":                 {"agent-skill@1/exposed", "agent-skills@1/destination"},
	"bundle-ignores-name":               {"agent-skill@1/exposed"},
	"bundle-drops-support":              {"agent-skill@1/exposed"},
	"bundle-loses-executable":           {"agent-skill@1/exposed"},
	"bundle-corrupts":                   {"agent-skill@1/exposed"},
	"bundle-skill-newline":              {"agent-skill@1/exposed"},
	"bundle-renamed-newline":            {"agent-skill@1/exposed"},
	"bundle-reference-newline":          {"agent-skill@1/exposed"},
	"bundle-reader-newline":             {"agent-skill@1/exposed"},
	"bundle-after-launch":               {"agent-skill@1/before-launch"},
	"bundle-executes":                   {"agent-skill@1/no-execution"},
	"bundle-accepts-unavailable":        {"agent-skill@1/unavailable"},
	"bundle-refuses-optional":           {"agent-skill@1/unavailable"},
	"bundle-drops-skip":                 {"agent-skill@1/unavailable"},
	"bundle-overwrites-existing":        {"agent-skill@1/existing-conflict"},

	"drops-image-env-defaults":                       {"SPEC-v3 §6/env-expanded"},
	"ignores-env-overrides":                          {"SPEC-v3 §6/env-expanded"},
	"drops-empty-env-overrides":                      {"SPEC-v3 §6/env-expanded"},
	"ssh-agent-create-workload-forward-refused-sign": {"ssh-agent@1/signatures-bounded"},
	"ssh-agent-restart-startup-forward-refused-sign": {"ssh-agent@1/signatures-bounded"},

	"ssh-agent-create-workload-unfiltered-sign":        {"ssh-agent@1/signatures-bounded"},
	"ssh-agent-create-workload-unfiltered-operations":  {"ssh-agent@1/operations-restricted"},
	"ssh-agent-create-startup-unfiltered-sign":         {"ssh-agent@1/signatures-bounded"},
	"ssh-agent-create-startup-unfiltered-operations":   {"ssh-agent@1/operations-restricted"},
	"ssh-agent-restart-workload-unfiltered-sign":       {"ssh-agent@1/signatures-bounded"},
	"ssh-agent-restart-workload-unfiltered-operations": {"ssh-agent@1/operations-restricted"},
	"ssh-agent-restart-startup-unfiltered-sign":        {"ssh-agent@1/signatures-bounded"},
	"ssh-agent-restart-startup-unfiltered-operations":  {"ssh-agent@1/operations-restricted"},
	"ssh-agent-install-unfiltered-operations":          {"ssh-agent@1/operations-restricted"},
	"ssh-agent-install-unfiltered-sign":                {"ssh-agent@1/signatures-bounded"},
	"ssh-agent-install-forward-refused-sign":           {"ssh-agent@1/signatures-bounded"},
	"ssh-agent-dual-unfiltered-runtime":                {"ssh-agent@1/signatures-bounded"},
	"ssh-agent-dual-runtime-only":                      {"ssh-agent@1/signatures-bounded"},
	"ssh-agent-dual-install-only":                      {"ssh-agent@1/signatures-bounded"},
	"install-socket-still-reachable":                   {"ssh-agent@1/phase-scoped"},

	"ssh-agent-restart-missing-workload-env": {"ssh-agent@1/every-boot"},
	"ssh-agent-restart-missing-startup-env":  {"ssh-agent@1/every-boot"},
	"ssh-agent-stale-boot-proofs":            {"ssh-agent@1/every-boot"},
	"ssh-agent-refusal-unnamed":              {"ssh-agent@1/unavailable-refuses-required"},
	"restricts-host-only-user":               {"ssh-agent@1/logins-bounded"},

	"ignores-ssh-agent":                     {"ssh-agent@1/agent-reachable", "ssh-agent@1/operations-restricted", "ssh-agent@1/every-boot"},
	"ssh-agent-drops-sign":                  {"ssh-agent@1/agent-reachable"},
	"ssh-agent-missing-workload-env":        {"ssh-agent@1/agent-reachable"},
	"ssh-agent-missing-startup-env":         {"ssh-agent@1/agent-reachable"},
	"ssh-agent-bogus-workload-socket":       {"ssh-agent@1/agent-reachable"},
	"ssh-agent-bogus-startup-socket":        {"ssh-agent@1/agent-reachable"},
	"ssh-agent-forwards-constrained-rsa":    {"ssh-agent@1/operations-restricted"},
	"ssh-agent-relays-everything":           {"ssh-agent@1/operations-restricted"},
	"ssh-agent-forwards-refused":            {"ssh-agent@1/operations-restricted"},
	"ssh-agent-initial-without-grant":       {"ssh-agent@1/absent-without-grant"},
	"ssh-agent-initial-optional-leak":       {"ssh-agent@1/unavailable-skips-optional"},
	"ssh-agent-without-grant":               {"ssh-agent@1/absent-without-grant"},
	"ssh-agent-install-missing":             {"ssh-agent@1/phase-scoped"},
	"leaves-install-ssh-agent-open":         {"ssh-agent@1/phase-scoped"},
	"leaks-install-ssh-agent-to-entrypoint": {"ssh-agent@1/phase-scoped"},
	"ssh-agent-first-boot-only":             {"ssh-agent@1/every-boot"},
	"ssh-agent-accepts-without-agent":       {"ssh-agent@1/unavailable-refuses-required"},
	"ssh-agent-refuses-optional":            {"ssh-agent@1/unavailable-skips-optional"},
	"ignores-sign-bounds":                   {"ssh-agent@1/signatures-bounded"},
	"drops-bound-signature":                 {"ssh-agent@1/signatures-bounded"},
	"forwards-unclassified":                 {"ssh-agent@1/signatures-bounded"},
	"ignores-login-bounds":                  {"ssh-agent@1/logins-bounded", "ssh-agent@1/binding-verified"},
	"drops-bound-login":                     {"ssh-agent@1/logins-bounded", "ssh-agent@1/binding-verified"},
	"ignores-hostbound-key":                 {"ssh-agent@1/logins-bounded"},
	"trusts-invalid-login-key":              {"ssh-agent@1/logins-bounded"},
	"ignores-login-user":                    {"ssh-agent@1/logins-bounded"},
	"ignores-session-id":                    {"ssh-agent@1/logins-bounded"},
	"trusts-sandbox-known-hosts":            {"ssh-agent@1/destination-keys-outside-sandbox"},
	"trusts-any-host-key":                   {"ssh-agent@1/logins-bounded"},
	"trusts-unverified-binding":             {"ssh-agent@1/binding-verified"},
	"trusts-forwarding-binding":             {"ssh-agent@1/binding-verified"},
	"identity-selected-null-source":         {"git-identity@1/unavailable-refuses-required"},
	"identity-selected-wrong-kit":           {"git-identity@1/unavailable-refuses-required"},
	"identity-selected-bad-workload":        {"git-identity@1/unavailable-refuses-required"},
	"identity-selected-missing-members":     {"git-identity@1/unavailable-refuses-required"},
	"leaks-identity-env":                    {"git-identity@1/absent-without-grant", "git-identity@1/unavailable-refuses-required"},
	"imports-effective-git-settings":        {"git-identity@1/identity-only"},
	"late-identity-workload-start":          {"git-identity@1/pinned-selection"},
	"late-identity-workload-recreate":       {"git-identity@1/pinned-selection"},
	"late-identity-restart-hook":            {"git-identity@1/before-hooks"},
	"late-identity-recreate-hook":           {"git-identity@1/before-hooks"},
	"skips-identity-restart-hook":           {"git-identity@1/before-hooks"},
	"skips-identity-recreate-hook":          {"git-identity@1/before-hooks"},
	"imports-identity-include":              {"git-identity@1/identity-only"},
	"imports-identity-filter":               {"git-identity@1/identity-only"},
	"imports-identity-signing-program":      {"git-identity@1/identity-only"},
	"imports-identity-signing-key":          {"git-identity@1/identity-only"},
	"identity-skip-wrong-path":              {"git-identity@1/unavailable-refuses-required"},
	"identity-skip-wrong-source-path":       {"git-identity@1/unavailable-refuses-required"},
	"identity-skip-missing-member-sources":  {"git-identity@1/unavailable-refuses-required"},
	"identity-skip-wrong-member-kit":        {"git-identity@1/unavailable-refuses-required"},
	"identity-skip-wrong-member-path":       {"git-identity@1/unavailable-refuses-required"},
	"ignores-git-identity":                  {"git-identity@1/global-defaults"},
	"corrupts-git-identity":                 {"git-identity@1/global-defaults"},
	"git-identity-after-launch":             {"git-identity@1/global-defaults"},
	"late-git-identity":                     {"git-identity@1/before-hooks"},
	"forces-git-identity":                   {"git-identity@1/local-precedence"},
	"imports-source-git-settings":           {"git-identity@1/identity-only"},
	"clobbers-guest-git-settings":           {"git-identity@1/identity-only"},
	"edits-identity-source":                 {"git-identity@1/source-unchanged"},
	"rereads-identity-on-recreate":          {"git-identity@1/pinned-selection"},
	"loses-git-identity-on-start":           {"git-identity@1/pinned-selection"},
	"imports-unrequested-identity":          {"git-identity@1/absent-without-grant"},
	"accepts-missing-identity":              {"git-identity@1/unavailable-refuses-required"},
	"refuses-optional-identity":             {"git-identity@1/unavailable-refuses-required"},
	"imports-unavailable-identity":          {"git-identity@1/unavailable-refuses-required"},
	"drops-identity-skip-record":            {"git-identity@1/unavailable-refuses-required"},
	"selects-unavailable-identity":          {"git-identity@1/unavailable-refuses-required"},
	"selects-and-skips-identity":            {"git-identity@1/unavailable-refuses-required"},
	"group-invalid-env-expanded":            {"SPEC-v3 §7.1.1/validate-expanded-declarations"},
	"group-skips-invalid-env-expanded":      {"SPEC-v3 §7.1.1/validate-expanded-declarations"},
	"group-invalid-expanded":                {"SPEC-v3 §7.1.1/validate-expanded-declarations"},
	"group-skips-invalid-expanded":          {"SPEC-v3 §7.1.1/validate-expanded-declarations"},
	"group-reselect-skipped-restart":        {"SPEC-v3 §7.1.1/lifetime"},
	"group-never-admits-recreate":           {"SPEC-v3 §7.1.1/lifetime"},
	"group-ignore-required-lifecycle":       {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-ignore-optional-lifecycle":       {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-selected-member-sources":         {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-skipped-member-sources":          {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-member-sources-local":            {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-wrong-source-path":               {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-wrong-source-kit":                {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-accepted-rejection":              {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-extra-rejection":                 {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-required-no-kit":                 {"SPEC-v3 §7.1.1/atomic-selection"},

	"ordinary-ignore-optional-rejection": {"SPEC-v3 §7.1.1/atomic-selection"},
	"ordinary-ignore-required-rejection": {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-extra-grant":                  {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-conflict-no-kit":              {"SPEC-v3 §7.1.1/conflicts"},
	"group-file-leak":                    {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-order":                        {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-records":                      {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-member-paths":                 {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-missing-ordinary-record":      {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-missing-independent-record":   {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-accept-required":              {"SPEC-v3 §7.1.1/atomic-selection"},
	"group-drop-conflict":                {"SPEC-v3 §7.1.1/conflicts"},
	"group-drop-interactive-conflict":    {"SPEC-v3 §7.1.1/conflicts"},
	"group-reselect-restart":             {"SPEC-v3 §7.1.1/lifetime"},
	"group-no-recreate":                  {"SPEC-v3 §7.1.1/lifetime"},
	"group-stale-recreate-records":       {"SPEC-v3 §7.1.1/lifetime"},
	"group-stale-recreate-hooks":         {"SPEC-v3 §7.1.1/lifetime"},
	"group-skip-failure":                 {"SPEC-v3 §7.1.1/execution"},

	"ignores-long-running-mixin":        {"long-running@1/survives-session-disconnect"},
	"stops-on-disconnect":               {"long-running@1/survives-session-disconnect"},
	"loses-background-on-disconnect":    {"long-running@1/survives-session-disconnect"},
	"restarts-background-on-disconnect": {"long-running@1/survives-session-disconnect"},
	"idle-errors":                       {"long-running@1/survives-session-disconnect"},
	"status-errors":                     {"long-running@1/survives-session-disconnect", "long-running@1/explicit-stop-honored"},
	"status-malformed":                  {"long-running@1/survives-session-disconnect", "long-running@1/explicit-stop-honored"},
	"ignores-explicit-stop":             {"long-running@1/explicit-stop-honored"},
	"refuses-optional-long-running":     {"conformance.md §2.2/optional-long-running-accepted"},
	"install-twice":                     {"lifecycle@1/install-once"},
	"no-startup":                        {"lifecycle@1/startup-every-boot"},
	"ignores-env-expansion":             {"lifecycle@1/files-written", "SPEC-v3 §6/env-expanded"},
	"ignores-files":                     {"lifecycle@1/files-written"},
	"writes-files-as-root":              {"lifecycle@1/files-written"},
	"writes-files-read-only":            {"lifecycle@1/files-written"},
	"leaks-env":                         {"lifecycle@1/hook-env-restricted"},
	"allows-everything":                 {"network-policy@1/deny-by-default", "network-policy@2/deny-by-default"},
	"ignores-http-method":               {"network-policy@2/http-method-enforced"},
	"ignores-http-path":                 {"network-policy@2/http-path-enforced"},
	"ignores-http-deny":                 {"network-policy@2/http-deny-precedence"},
	"leaves-install-egress-open": {
		"network-policy@1/install-phase-scoped",
		"network-policy@2/install-phase-scoped",
	},
	"leaks-undeclared-env":                  {"lifecycle@1/hook-env-restricted"},
	"copies-host-baseline":                  {"lifecycle@1/hook-env-restricted"},
	"copies-host-hostname":                  {"lifecycle@1/hook-env-restricted"},
	"copies-host-home":                      {"lifecycle@1/hook-env-restricted"},
	"copies-host-pwd":                       {"lifecycle@1/hook-env-restricted"},
	"leaks-secret-elsewhere":                {"credential@1/secret-absent-in-sandbox"},
	"leaks-secret":                          {"credential@1/secret-absent-in-sandbox"},
	"leaks-secret-decorated":                {"credential@1/secret-absent-in-sandbox"},
	"credential-first-phase-only":           {"credential@1/sentinel-phase-scoped"},
	"credential-last-phase-only":            {"credential@1/sentinel-phase-scoped"},
	"credential-install-at-runtime":         {"credential@1/sentinel-phase-scoped"},
	"credential-runtime-at-install":         {"credential@1/sentinel-phase-scoped"},
	"credential-phase-leaks-secret":         {"credential@1/sentinel-phase-scoped"},
	"credential-install-at-entrypoint":      {"credential@1/sentinel-phase-scoped"},
	"credential-missing-at-entrypoint":      {"credential@1/sentinel-phase-scoped"},
	"no-credential":                         {"credential@1/secret-absent-in-sandbox"},
	"leaks-inject-only-env":                 {"credential@1/inject-only-no-env"},
	"hides-inject-only-in-existing-env":     {"credential@1/inject-only-no-env"},
	"hides-inject-only-in-token":            {"credential@1/inject-only-no-env"},
	"leaks-plain-inject-only-env":           {"credential@1/inject-only-no-env"},
	"exposes-install-inject-only":           {"credential@1/inject-only-no-env"},
	"hides-install-inject-only-in-declared": {"credential@1/inject-only-no-env"},
	"hides-inject-only-in-export":           {"credential@1/inject-only-no-env"},
	"assumes-uid-1000":                      {"sbx@1/honors-image-user"},
	"runs-image-entrypoint":                 {"sbx@1/entrypoint-not-pid-one"},
	"workspace-at-fixed-path":               {"sbx@1/workspace-at-workdir"},
	"accept-unknown":                        {"SPEC-v3 §7.3/unknown-required-refused"},
	"refuses-everything":                    {"conformance.md §2.1/baseline-create-succeeds"},
	"no-context":                            {"agent-context@1/body-readable", "agent-context@1/directory-honored"},
	"ignores-context-directory":             {"agent-context@1/directory-honored"},
	"ignores-profile-filename":              {"agent-context@1/directory-honored"},
	"workload-profile-wins":                 {"agent-context@1/directory-honored"},
	"overwrites-context-profile":            {"agent-context@1/directory-honored"},
	"accepts-context-conflict":              {"agent-context@1/explicit-profile-conflict"},
	"context-at-fixed-directory":            {"agent-context@1/directory-honored"},
	"loses-volume":                          {"volume@1/persists-across-recreate"},
	"loses-volume-on-restart":               {"volume@1/persists-across-recreate"},
	"keeps-writable-layer":                  {"volume@1/persists-across-recreate"},
	"drops-context-body":                    {"agent-context@1/body-readable"},
	"ignores-privileged":                    {"privileged@1/elevation-granted"},
	"ignores-skills":                        {"agent-skills@1/store-mounted-at-declared-path"},
	"empty-skills":                          {"agent-skills@1/store-mounted-at-declared-path"},
	"wrong-skill":                           {"agent-skills@1/store-mounted-at-declared-path"},
	"first-skills-only":                     {"agent-skills@1/store-mounted-at-declared-path"},
	"writable-decoy":                        {"agent-skills@1/readwrite-granted-when-both-allow"},
	"mounts-skills-late":                    {"agent-skills@1/mounted-before-hooks"},
	"narrows-shared-path":                   {"agent-skills@1/shared-path-widest-mode"},
	"ignores-host-readonly":                 {"agent-skills@1/host-readonly-narrows"},
	"refuses-missing-required-skills":       {"agent-skills@1/host-store-missing"},
	"refuses-missing-optional-skills":       {"agent-skills@1/host-store-missing"},
	"skips-missing-required-skills":         {"agent-skills@1/host-store-missing"},
	"skips-missing-optional-skills":         {"agent-skills@1/host-store-missing"},
	"bundle-missing-drops-required":         {"agent-skills@1/bundled-with-missing-store"},
	"bundle-missing-drops-optional":         {"agent-skills@1/bundled-with-missing-store"},
	"bundle-missing-late":                   {"agent-skills@1/bundled-with-missing-store"},
	"refuses-empty-required-skills":         {"agent-skills@1/host-store-empty"},
	"refuses-empty-optional-skills":         {"agent-skills@1/host-store-empty"},
	"skips-empty-required-skills":           {"agent-skills@1/host-store-empty"},
	"skips-empty-optional-skills":           {"agent-skills@1/host-store-empty"},
	"bundle-empty-drops-required":           {"agent-skills@1/bundled-with-empty-store"},
	"bundle-empty-drops-optional":           {"agent-skills@1/bundled-with-empty-store"},
	"bundle-empty-late":                     {"agent-skills@1/bundled-with-empty-store"},
	"refuses-required-skills":               {"agent-skills@1/host-store-optional"},
	"ignores-host-off":                      {"agent-skills@1/host-store-optional"},
	"skips-required-skills":                 {"agent-skills@1/host-store-optional"},
	"skips-optional-skills":                 {"agent-skills@1/host-off-keeps-optional"},
	"refuses-optional-skills":               {"agent-skills@1/host-off-keeps-optional"},
	"mounts-optional-despite-off":           {"agent-skills@1/host-off-keeps-optional"},
	"mounts-later-skills-late":              {"agent-skills@1/mounted-before-hooks"},
	"separate-stores":                       {"agent-skills@1/same-store-at-every-path"},
	"ignores-readonly":                      {"agent-skills@1/readonly-default-honored"},
	"ignores-readwrite":                     {"agent-skills@1/readwrite-granted-when-both-allow"},
}

func TestEachCheckFailsWhenItsBehaviorIsAbsent(t *testing.T) {
	// Host claims must not skip the checks these mutations exercise.
	t.Setenv("KIT_TCK_FAKE_CLAIMS", "com.docker.sandbox/volume@1")

	byRequirement := make(map[string]check, len(checks))
	for _, c := range checks {
		byRequirement[c.requirement] = c
	}
	for broken, requirements := range mutations {
		t.Run(broken, func(t *testing.T) {
			t.Parallel()
			// Every mutation still has to fail every requirement it names.
			// Repeating unrelated checks here makes suite growth quadratic;
			// TestAConformingRuntimePasses retains the complete baseline.
			require.NotEmpty(t, requirements)
			selected := make([]check, 0, len(requirements))
			for _, requirement := range requirements {
				c, ok := byRequirement[requirement]
				require.True(t, ok, "mutation %q names unknown requirement %q", broken, requirement)
				selected = append(selected, c)
			}
			rep := runAgainstFake(t, broken, selected...)
			failed := failedRequirements(rep)
			for _, requirement := range requirements {
				require.Contains(t, failed, requirement,
					"breaking %q must fail %s:\n%s", broken, requirement, rep)
			}
		})
	}
}

// A partial implementation is conforming for what it claims, so unclaimed
// capabilities are skipped rather than failed — but the refusal
// requirement still applies, because that is the one thing a runtime must
// do about capabilities it lacks.
func TestUnclaimedCapabilitiesAreSkipped(t *testing.T) {
	t.Parallel()
	rep := runAgainstFake(t, "claims-nothing")
	require.False(t, rep.Failed(), "unclaimed capabilities must not fail:\n%s", rep)

	var skipped int
	for _, f := range rep.Findings {
		if f.Severity == report.Skip {
			skipped++
		}
	}
	require.Positive(t, skipped, "capability checks should have been skipped")
}

// The coverage guard compares these against the specification, so every
// check has to name a requirement and name it once.
func TestRequirementsAreUniqueAndNamed(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Requirements() {
		require.NotEmpty(t, r)
		require.False(t, seen[r], "duplicate requirement id %q", r)
		seen[r] = true
	}
}

// A check with no mutation case is a check nobody has seen fail, so the
// mutation map has to keep pace with the suite. resources@1 is excused
// because its page says SHOULD: the check warns, and a warning is not a
// failure to provoke.
func TestEveryCheckHasAMutationCase(t *testing.T) {
	excused := map[string]string{
		"resources@1/limit-applied":                      "the page says SHOULD, so the check warns rather than fails",
		"conformance.md §2.2/required-unclaimed-refused": "needs a claims subset the mutation table cannot express; TestARequiredUnclaimedTypeMustBeRefused drives it",
	}

	covered := map[string]bool{}
	for _, requirements := range mutations {
		for _, requirement := range requirements {
			covered[requirement] = true
		}
	}
	for _, r := range Requirements() {
		if covered[r] || excused[r] != "" {
			continue
		}
		t.Errorf("requirement %q has no mutation case: add one to the fake adapter, or excuse it with a reason", r)
	}
}

// The refusal signal has to be distinguishable from failure, or a runtime
// that cannot create anything would satisfy every requirement that is met
// by refusing. Here the runtime declines the right kit for the right
// reason but reports it as an error, which must not count.
func TestAFailingCreateIsNotMistakenForARefusal(t *testing.T) {
	t.Parallel()
	rep := runAgainstFake(t, "refusal-as-error")
	require.Contains(t, failedRequirements(rep), "SPEC-v3 §7.3/unknown-required-refused",
		"a create that fails for unrelated reasons must not count as a refusal:\n%s", rep)
}

func TestSSHAgentReachabilityWithoutLifecycle(t *testing.T) {
	for _, broken := range []string{"", "ssh-agent-missing-workload-env", "ssh-agent-bogus-workload-socket"} {
		t.Run(broken, func(t *testing.T) {
			a := adapter.New(filepath.Join("testdata", "fake-adapter"))
			a.Env = []string{
				"KIT_TCK_FAKE_STATE=" + t.TempDir(),
				"KIT_TCK_FAKE_CLAIMS=" + capSSHAgent,
				"KIT_TCK_FAKE_BROKEN=" + broken,
			}
			rep, err := Run(t.Context(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)})
			require.NoError(t, err)
			found := false
			for _, f := range rep.Findings {
				if f.Requirement == "ssh-agent@1/agent-reachable" {
					found = true
					require.Equal(t, report.Fail, f.Severity)
				}
			}
			require.Equal(t, broken != "", found, "%s", rep)
			if broken == "" {
				require.False(t, rep.Failed(), "%s", rep)
			}
		})
	}
}

func TestGitIdentityNeedsNoHelperCapabilities(t *testing.T) {
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + capGitIdentity, "KIT_TCK_FAKE_BROKEN="}
	rep, err := Run(context.Background(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)})
	require.NoError(t, err)
	require.False(t, rep.Failed(), "git-identity alone must be testable:\n%s", rep)
	for _, f := range rep.Findings {
		if f.Requirement != "git-identity@1/before-hooks" {
			require.NotContains(t, f.Requirement, "git-identity@1", "%s", f)
		}
	}
}
