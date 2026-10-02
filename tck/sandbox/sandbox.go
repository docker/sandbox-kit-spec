package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

// Env is what a check runs against: the runtime under test, the kits it
// can be asked to compose, and the types it admits to implementing.
type Env struct {
	Adapter *adapter.Adapter
	// Fixtures resolves a fixture name to the reference the runtime
	// accepts, so the suite does not care how kits reach it.
	Fixtures func(name string) string
	// Claimed is what `capabilities` reported.
	Claimed map[string]bool
	// Secret is the value bound for the fixture credential service, so a
	// leak is detected by comparison rather than by how it is spelled.
	Secret string

	// leaked records sandboxes cleanup could not remove, so a suite that
	// left state behind says so instead of hiding it.
	leaked []string
	// Host-directory cleanup is accounted for separately from sandbox rm:
	// the capability intentionally keeps these directories after rm.
	hostMountLeaks []string
}

// The suite arranges three known values before it judges anything, because
// several requirements are about what must NOT appear and absence cannot
// be judged against an unknown.
const (
	// HostSentinelName and HostSentinelValue are placed in the adapter's
	// environment. A hook that sees the value inherited its environment
	// rather than receiving only its declared names.
	HostSentinelName  = "KIT_TCK_HOST_SENTINEL"
	HostSentinelValue = "kit-tck-host-sentinel-must-not-leak"

	// SecretValue is what an adapter binds for the fixture credential
	// service. The container must see a sentinel instead, and comparing
	// against a known value detects a leak however it is spelled.
	SecretName  = "KIT_TCK_BOUND_SECRET"
	SecretValue = "kit-tck-bound-secret-must-not-reach-the-sandbox"

	// StagedKitRoot is where a kit's own sources and context body live in
	// the composed image, which is what a profile reference points at.
	StagedKitRoot = "/usr/share/sandbox/kit"

	// SkillName is a skill the adapter must have in the host's shared
	// store, with skills enabled, before claiming agent-skills@1. Without
	// a known entry the check cannot tell a mounted store from an empty
	// directory, and cannot tell a runtime that ignored the capability
	// from a host that legitimately had nothing to share.
	SkillNameVar = "KIT_TCK_SKILL_NAME"
	SkillName    = "kit-tck-marker-skill"

	// ProfilePath is where the workload fixture's agent-context filename
	// lands: the fixture declares WORKDIR /home/agent/workspace, and the
	// profile sits beside that workspace rather than inside it, so it is
	// the workspace directory's sibling — a fixture-pinned location, not
	// an assumption about where the runtime keeps workspaces in general.
	ProfilePath = "/home/agent/AGENTS.md"

	// ContextBody is what the context fixture stages, so the check can
	// verify the body behind the profile's reference and not just the
	// reference itself.
	ContextBody = "Conformance fixture context body.\n"
)

// sentinelHome returns a sentinel-named path that resolves to the real
// home directory: functional for everything the adapter runs, and marked
// so a hook environment holding its value is holding host provenance.
func sentinelHome() (string, func(), error) {
	real, err := os.UserHomeDir()
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "kit-tck-*")
	if err != nil {
		return "", nil, err
	}
	link := filepath.Join(dir, HostSentinelValue)
	if err := os.Symlink(real, link); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	return link, func() { _ = os.RemoveAll(dir) }, nil
}

// cleanupTimeout bounds sandbox removal, which runs after a check has
// finished and must not hang the suite.
const cleanupTimeout = 2 * time.Minute

// claims reports whether the runtime implements a capability type. A type
// it never claimed is not a failure — a partial implementation is
// conforming for what it claims — so its checks are skipped.
func (e *Env) claims(capabilityType string) bool { return e.Claimed[capabilityType] }

// sandbox creates a sandbox and guarantees its removal, so a failing check
// never strands state for the next one.
func (e *Env) sandbox(ctx context.Context, kits []string, args map[string]string) (string, func(), error) {
	return e.sandboxWith(ctx, kits, adapter.CreateOptions{Args: args})
}

func (e *Env) sandboxWith(ctx context.Context, kits []string, opts adapter.CreateOptions) (string, func(), error) {
	refs := make([]string, 0, len(kits))
	for _, name := range kits {
		refs = append(refs, e.Fixtures(name))
	}
	id, err := e.Adapter.Create(ctx, refs, opts)
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() {
		// Removal must survive the check's context: a cancelled or timed
		// out check is exactly when a sandbox would otherwise be left
		// behind, and the contract promises the suite removes what it
		// creates.
		rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if err := e.Adapter.Remove(rmCtx, id); err != nil {
			e.leaked = append(e.leaked, fmt.Sprintf("%s: %v", id, err))
		}
	}
	return id, cleanup, nil
}

// check is one requirement, named by the statement it judges.
type check struct {
	// requirement is the id the coverage guard matches against the
	// capability pages, such as "lifecycle@1/install-once".
	requirement string
	// capability is the type a runtime must claim for this to apply.
	capability string
	// needs are further claims this check's observation depends on — a
	// skills check that reads what an install hook recorded consumes
	// lifecycle@1 too. Fixtures declare such helper capabilities optional,
	// so an unclaiming runtime composes fine and the check skips here
	// instead of failing on an observation the runtime never promised.
	needs []string
	run   func(context.Context, *Env) []report.Finding
}

// firstUnclaimed names the claim this check is gated on that the runtime
// does not make, or "" when the check applies.
func (c check) firstUnclaimed(e *Env) string {
	if c.capability != "" && !e.claims(c.capability) {
		return c.capability
	}
	for _, n := range c.needs {
		if !e.claims(n) {
			return n
		}
	}
	return ""
}

// Run judges a runtime against every check it claims to be subject to.
func Run(ctx context.Context, e *Env) (report.Report, error) {
	return runChecks(ctx, e, checks)
}

// Mutation tests judge only the requirements each mutation targets, but need
// the same claim gating, sentinel environment, and cleanup as a complete run.
func runChecks(ctx context.Context, e *Env, selected []check) (rep report.Report, err error) {
	if e.Adapter == nil {
		return report.Report{}, fmt.Errorf("no adapter to test")
	}
	if e.Fixtures == nil {
		return report.Report{}, fmt.Errorf("no fixture resolver; use Fixtures(root) or MaterializeFixtures")
	}
	// The adapter learns the known values from its environment: that is
	// how it binds the secret, seeds the skill, and how the sentinel gets
	// somewhere a leaking runtime would pick it up. Resolve the secret
	// first and hand over that same value — a
	// caller-supplied secret bound as the constant instead would leave the
	// leak check comparing against something never bound, and a leaked
	// value would pass.
	if e.Secret == "" {
		e.Secret = SecretValue
	}
	// Baseline variables are decorated with the sentinel too: the
	// lifecycle page permits their NAMES in a hook but requires values
	// derived from the image and sandbox, and names alone cannot tell an
	// image PATH from the host's. A runtime copying any of them into a
	// hook now carries the sentinel into the same value scan that catches
	// every other leak. PATH keeps its real components, and HOME and PWD
	// stay FUNCTIONAL while still marked: both point through a
	// sentinel-named symlink to the real home, so credential helpers and
	// relative invocation keep working while the value betrays its
	// provenance.
	markedHome, cleanupHome, err := sentinelHome()
	if err != nil {
		return report.Report{}, err
	}
	defer cleanupHome()
	// The caller's Adapter is borrowed, not owned: without restoring it,
	// a reuse after Run would inherit the suite's secret in Env and a
	// Dir pointing at the removed sentinel home.
	savedEnv, savedDir := e.Adapter.Env, e.Adapter.Dir
	defer func() { e.Adapter.Env, e.Adapter.Dir = savedEnv, savedDir }()
	e.Adapter.Dir = markedHome
	e.Adapter.Env = append(e.Adapter.Env,
		HostSentinelName+"="+HostSentinelValue,
		SecretName+"="+e.Secret,
		SkillNameVar+"="+SkillName,
		"TERM="+HostSentinelValue,
		"HOSTNAME="+HostSentinelValue,
		"OLDPWD=/"+HostSentinelValue,
		"SHLVL="+HostSentinelValue,
		"_=/"+HostSentinelValue,
		"HOME="+markedHome,
		"PWD="+markedHome,
		"PATH="+os.Getenv("PATH")+string(os.PathListSeparator)+"/"+HostSentinelValue)

	if e.Claimed == nil {
		claimed, err := e.Adapter.Capabilities(ctx)
		if err != nil {
			return report.Report{}, err
		}
		e.Claimed = map[string]bool{}
		for _, c := range claimed {
			e.Claimed[c] = true
		}
	}

	defer func() {
		for _, l := range e.leaked {
			// A failure, not a warning: §2.4 promises the suite removes
			// what it creates, and an adapter whose rm does not work is
			// incomplete however its checks went.
			rep.Add("cleanup", "conformance.md §2.4",
				report.Failf("a sandbox could not be removed: %s", l))
		}
		for _, l := range e.hostMountLeaks {
			rep.Add("cleanup", "conformance.md §2.4", report.Failf("a host directory could not be removed: %s", l))
		}
	}()
	for _, c := range selected {
		if unclaimed := c.firstUnclaimed(e); unclaimed != "" {
			rep.Add(c.requirement, c.requirement,
				report.Skipf("runtime does not claim %s", unclaimed))
			continue
		}
		rep.Add(c.requirement, c.requirement, c.run(ctx, e)...)
		// A deadline that expired inside the check produced failure
		// findings about whatever it interrupted; reporting them as the
		// runtime's non-conformance would misattribute the harness's own
		// timeout, so the context error wins.
		if err := ctx.Err(); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// Requirements lists every requirement the suite judges, which is what the
// coverage guard compares against the specification.
func Requirements() []string {
	out := make([]string, 0, len(checks))
	for _, c := range checks {
		out = append(out, c.requirement)
	}
	return out
}

// httpAllowed asserts one HTTP request reaches its target, so a later
// refusal reads as the rule that refused it rather than as a host the
// sandbox could never reach.
func httpAllowed(ctx context.Context, e *Env, id, method, url string) *report.Finding {
	res, err := e.Adapter.Exec(ctx, id, "kit-tck-http-probe", method, url)
	if err != nil {
		return failing("probe allowed request: %v", err)
	}
	if res.ExitCode != 0 {
		return failing("%s %s matches an http allow rule but was refused: %s",
			method, url, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// httpRefused asserts one HTTP request is refused the way the page
// requires: exit 7 is the probe observing a 403 at the boundary. Every
// other status is a failure, including a dropped or timed-out
// connection — refusing by hanging up is not conforming, and accepting
// it here would also pass a runtime enforcing nothing over a network
// that happened to be broken.
func httpRefused(ctx context.Context, e *Env, id, method, url, whenAccepted string) []report.Finding {
	res, err := e.Adapter.Exec(ctx, id, "kit-tck-http-probe", method, url)
	if err != nil {
		return []report.Finding{*failing("probe refused request: %v", err)}
	}
	switch res.ExitCode {
	case 0:
		return []report.Finding{*failing("%s: %s %s", whenAccepted, method, url)}
	case 7:
		return nil
	default:
		return []report.Finding{*failing(
			"%s %s was not answered with 403; the probe exited %d, so the refusal was neither observed nor conforming: %s",
			method, url, res.ExitCode, strings.TrimSpace(res.Stderr))}
	}
}

func failing(format string, args ...any) *report.Finding {
	f := report.Failf(format, args...)
	return &f
}

// execOutput runs a command and returns its stdout untouched — trimming
// here would hide corruption from checks that compare file bytes, such as
// a runtime appending a newline to a declared file. Callers comparing
// counts or single tokens normalize explicitly.
func execOutput(ctx context.Context, e *Env, id string, argv ...string) (string, *report.Finding) {
	res, err := e.Adapter.Exec(ctx, id, argv...)
	if err != nil {
		f := report.Failf("exec %s: %v", strings.Join(argv, " "), err)
		return "", &f
	}
	if res.ExitCode != 0 {
		f := report.Failf("exec %s: exit %d: %s", strings.Join(argv, " "), res.ExitCode, strings.TrimSpace(res.Stderr))
		return "", &f
	}
	return res.Stdout, nil
}

// FixtureDir is where the suite's own kits live, relative to this package.
const FixtureDir = "testdata/fixtures"

// Fixtures resolves a fixture name to a kit directory under root. The
// suite ships its kits rather than naming kits a runtime is expected to
// already have: a conformance suite that depends on the subject providing
// its test material proves nothing.
func Fixtures(root string) func(string) string {
	// Resolved eagerly: the suite runs adapters under its own working
	// directory, so a relative root captured here would resolve under
	// the sentinel home instead of the caller's directory.
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return func(name string) string { return filepath.Join(root, name) }
}

// DefaultFixtureRoot locates the shipped fixtures for a caller running
// outside this package's directory.
// DefaultFixtureRoot returns the fixtures root and a cleanup for whatever
// it created. A checkout's fixtures win when present, so local fixture
// edits are picked up; everywhere else the embedded copy makes an
// installed binary self-sufficient.
func DefaultFixtureRoot() (string, func(), error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", nil, err
	}
	for {
		candidate := filepath.Join(dir, "tck", "sandbox", FixtureDir)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, func() {}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return MaterializeFixtures()
		}
		dir = parent
	}
}
