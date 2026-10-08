package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Adapter is a candidate runtime under test.
type Adapter struct {
	// Path is the executable implementing the contract.
	Path string
	// Env is added to the adapter's environment.
	Env []string
	// Dir, when set, is the working directory verbs run in. The suite
	// points it at a sentinel-named path so a runtime copying the host's
	// PWD into a hook carries the sentinel with it.
	Dir string
}

// Result is one adapter invocation's outcome. A non-zero ExitCode is not
// an error by itself: a test asserting that something is refused needs the
// refusal, not a failure to run.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// New returns an adapter rooted at an executable.
func New(path string) *Adapter { return &Adapter{Path: path} }

// Capabilities lists the types the runtime claims to implement. Tests for
// anything absent are skipped, except the one asserting that a required
// unclaimed type is refused.
func (a *Adapter) Capabilities(ctx context.Context) ([]string, error) {
	res, err := a.run(ctx, "capabilities")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("capabilities: exit %d: %s", res.ExitCode, res.Stderr)
	}
	var types []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			types = append(types, line)
		}
	}
	sort.Strings(types)
	return types, nil
}

// CreateOptions shape one create call beyond the kit set itself.
type CreateOptions struct {
	// Name is a suite-owned alias, not the sandbox's storage identity.
	Name string
	// GitIdentityConfig transports a suite-owned test binding in Git config
	// format, or "off" to withhold it. It does not prescribe runtime storage.
	GitIdentityConfig string
	// RejectCapabilities arranges false selector answers for these types.
	RejectCapabilities []string

	// Args are kit argument overrides, passed as --arg name=value.
	Args map[string]string

	// Env overrides the final container environment, passed as --env name=value.
	// These values take precedence over image defaults and Kit argument exports.
	Env map[string]string

	// SkillsHostMode, when set, is the host-side skills setting the
	// adapter must arrange for this sandbox, passed as
	// --skills-host-mode. Empty means the contract's default: the most
	// permissive setting. The host's half of the access bound is
	// unobservable without this, because a suite that always arranges a
	// permissive host can only ever see the kit's half narrow.
	SkillsHostMode string

	// SkillsHostStore arranges a "missing" or "empty" host store independently
	// of sharing policy. Empty uses the normal seeded store. The override is
	// per sandbox and must not remove or alter user content.
	SkillsHostStore string

	// SSHAgent, when set, is the socket of the backing SSH agent the
	// runtime has available for this sandbox, passed as --ssh-agent: an
	// adapter wires it however its runtime obtains agents (forwarded from
	// a client, or managed). The suite runs its own agent with a key it
	// generated, so what a sandbox can do to a user's agent is observable
	// without touching the user's. Empty means no backing agent is
	// available.
	SSHAgent string

	// SSHKnownHosts, when set, is a known_hosts file naming the host keys
	// the runtime is to trust for ssh-agent@1 authenticate destinations,
	// passed as --ssh-known-hosts. The suite generates the keys for a test
	// server under a reserved name, so a relay matching session bindings
	// to them is observable without a real server; the page requires those
	// keys to come from outside the sandbox, which is where this is.
	SSHKnownHosts string
}

// Create composes a kit set into a running sandbox and returns its id.
func (a *Adapter) Create(ctx context.Context, kits []string, opts CreateOptions) (string, error) {
	argv := append([]string{"create"}, kits...)
	if opts.Name != "" {
		argv = append(argv, "--name", opts.Name)
	}
	if opts.GitIdentityConfig != "" {
		argv = append(argv, "--git-identity-config", opts.GitIdentityConfig)
	}
	for _, typ := range opts.RejectCapabilities {
		argv = append(argv, "--reject-capability", typ)
	}
	if opts.SkillsHostMode != "" {
		argv = append(argv, "--skills-host-mode", opts.SkillsHostMode)
	}
	if opts.SkillsHostStore != "" {
		argv = append(argv, "--skills-host-store", opts.SkillsHostStore)
	}
	if opts.SSHAgent != "" {
		argv = append(argv, "--ssh-agent", opts.SSHAgent)
	}
	if opts.SSHKnownHosts != "" {
		argv = append(argv, "--ssh-known-hosts", opts.SSHKnownHosts)
	}
	for _, name := range sortedKeys(opts.Args) {
		argv = append(argv, "--arg", name+"="+opts.Args[name])
	}
	for _, name := range sortedKeys(opts.Env) {
		argv = append(argv, "--env", name+"="+opts.Env[name])
	}
	res, err := a.run(ctx, argv...)
	if err != nil {
		return "", err
	}
	switch res.ExitCode {
	case 0:
	case ExitRefused:
		return "", &RefusedError{Detail: strings.TrimSpace(res.Stderr)}
	default:
		return "", fmt.Errorf("create: exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	id := strings.TrimSpace(res.Stdout)
	if id == "" {
		return "", errors.New("create succeeded but printed no sandbox id")
	}
	return id, nil
}

// Exec runs a command inside a sandbox, proxying its exit status.
func (a *Adapter) Exec(ctx context.Context, id string, argv ...string) (Result, error) {
	return a.run(ctx, append([]string{"exec", id, "--"}, argv...)...)
}

// WaitIdle exercises final-session disconnection and waits beyond the
// runtime's own auto-stop grace period without keeping the sandbox alive.
func (a *Adapter) WaitIdle(ctx context.Context, id string) error {
	return a.mustRun(ctx, "wait-idle", id)
}

// Status observes without attaching: Exec could implicitly restart a
// sandbox and hide the auto-stop the long-running check is looking for.
func (a *Adapter) Status(ctx context.Context, id string) (string, error) {
	res, err := a.run(ctx, "status", id)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("status: exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	state := strings.TrimSpace(res.Stdout)
	if state != "running" && state != "stopped" {
		return "", fmt.Errorf("status: expected running or stopped, got %q", state)
	}
	return state, nil
}

// Recreate replaces the sandbox's container — a fresh writable layer —
// preserving instance-owned block storage, including unmounted paths.
// Stop/start preserves the entire
// filesystem, so it cannot tell a volume from an ordinary directory;
// recreate is the observation that can.
func (a *Adapter) Recreate(ctx context.Context, id string) error {
	return a.RecreateWith(ctx, id, nil, nil)
}

// RecreateWith changes the composition within the existing instance.
// Kits replace the previous set when nonempty; args override retained
// argument values. Create-time host bindings remain unchanged.
func (a *Adapter) RecreateWith(ctx context.Context, id string, kits []string, args map[string]string) error {
	argv := append([]string{"recreate", id}, kits...)
	for _, name := range sortedKeys(args) {
		argv = append(argv, "--arg", name+"="+args[name])
	}
	res, err := a.run(ctx, argv...)
	if err != nil {
		return err
	}
	if res.ExitCode == ExitRefused {
		return &RefusedError{Detail: strings.TrimSpace(res.Stderr)}
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("recreate: exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// VolumePaths observes every allocated destination, mounted or retained.
// A removed instance returns an empty list without provisioning storage.
func (a *Adapter) VolumePaths(ctx context.Context, id string) ([]string, error) {
	res, err := a.run(ctx, "volume-paths", id)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("volume-paths: exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	var paths []string
	if err := json.Unmarshal([]byte(res.Stdout), &paths); err != nil {
		return nil, fmt.Errorf("volume-paths: %w", err)
	}
	if strings.TrimSpace(res.Stdout) == "null" {
		return nil, errors.New("volume-paths: expected a JSON array")
	}
	return paths, nil
}

// Stop and Start bracket a reboot, which is what separates install hooks
// from startup hooks.
func (a *Adapter) Stop(ctx context.Context, id string) error  { return a.mustRun(ctx, "stop", id) }
func (a *Adapter) Start(ctx context.Context, id string) error { return a.mustRun(ctx, "start", id) }

// Remove discards a sandbox.
func (a *Adapter) Remove(ctx context.Context, id string) error { return a.mustRun(ctx, "rm", id) }

// ExitRefused is the status an adapter returns when the runtime declined a
// request it understood. Any other non-zero status is a failure, and the
// difference matters: requirements satisfied by refusing something would
// otherwise be satisfied equally by an adapter that cannot run at all.
const ExitRefused = 2

// RefusedError reports a request the runtime declined deliberately.
type RefusedError struct {
	Detail string
}

func (e *RefusedError) Error() string {
	return "runtime refused the request: " + e.Detail
}

func (a *Adapter) mustRun(ctx context.Context, argv ...string) error {
	res, err := a.run(ctx, argv...)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s: exit %d: %s", argv[0], res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

func (a *Adapter) run(ctx context.Context, argv ...string) (Result, error) {
	// A relative adapter path is the caller's, resolved against the
	// caller's working directory — Dir must not retarget it.
	path := a.Path
	if a.Dir != "" && !filepath.IsAbs(path) && strings.ContainsRune(path, os.PathSeparator) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	cmd := exec.CommandContext(ctx, path, argv...)
	cmd.Dir = a.Dir
	// Killing the adapter does not close pipes a descendant runtime
	// process still holds; without a wait bound, a hung descendant
	// defeats every timeout above this call.
	cmd.WaitDelay = 30 * time.Second
	cmd.Env = append(cmd.Environ(), a.Env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		// Cancellation surfaces as an ExitError too, and classifying it
		// by exit code would turn the caller's deadline into a runtime
		// non-conformance finding.
		if ctx.Err() != nil {
			return res, fmt.Errorf("run adapter %s %s: %w", a.Path, argv[0], ctx.Err())
		}
		res.ExitCode = exitErr.ExitCode()
	default:
		// The adapter itself could not be run: not a verdict about the
		// runtime, so it surfaces as an error rather than a failure.
		return res, fmt.Errorf("run adapter %s %s: %w", a.Path, argv[0], err)
	}
	return res, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
