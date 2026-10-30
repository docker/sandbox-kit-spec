package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const (
	capDockerd = "com.docker.sandbox/dockerd@1"
	// fixtureDocker requests the engine and records, from a startup hook,
	// which engine start it saw. fixtureDockerWorkload ships a stand-in
	// dockerd on PATH and requests nothing.
	fixtureDocker         = "dockerd"
	fixtureDockerWorkload = "docker-workload"
	// fixtureDockerManual requests the engine with autoStart false and
	// keeps a token in the store from a root startup hook.
	fixtureDockerManual = "dockerd-manual"
	// fixtureDockerStoreVolume asks for a volume at the engine store.
	fixtureDockerStoreVolume = "dockerd-store-volume"

	// What the stand-in engine publishes each time it starts, the token it
	// keeps in its store, and what the startup hook appends once per boot.
	dockerBootRecord    = "/var/tmp/kit-tck-docker/boot"
	dockerArgvRecord    = "/var/tmp/kit-tck-docker/argv"
	dockerDaemonConfig  = "/etc/docker/daemon.json"
	dockerStoreToken    = "/var/lib/docker/kit-tck-storage"
	dockerStartupRecord = "/var/tmp/kit-tck-docker-startup"
	dockerManualToken   = "/var/lib/docker/kit-tck-manual-storage"
)

var (
	dockerComposition       = []string{fixtureDockerWorkload, fixtureDocker}
	dockerManualComposition = []string{fixtureDockerWorkload, fixtureDockerManual}
)

var dockerChecks = []check{
	{requirement: "dockerd@1/elevation-granted", capability: capDockerd, run: dockerElevation},
	{requirement: "dockerd@1/storage-private-and-persistent", capability: capDockerd, run: dockerStorage},
	{requirement: "dockerd@1/storage-removed-with-sandbox", capability: capDockerd, run: dockerRemoval},
	{requirement: "dockerd@1/engine-store-owned", capability: capDockerd, needs: []string{capVolume}, run: dockerStoreOwned},
	{requirement: "dockerd@1/daemon-json-honored", capability: capDockerd, run: dockerDaemonJSON},
	{requirement: "dockerd@1/engine-before-startup", capability: capDockerd, needs: []string{capLifecycle}, run: dockerBeforeStartup},
	{requirement: "dockerd@1/absent-without-grant", capability: capDockerd, run: dockerAbsentWithoutGrant},
	{requirement: "dockerd@1/manual-elevation-granted", capability: capDockerd, run: func(ctx context.Context, e *Env) []report.Finding {
		return dockerElevated(ctx, e, dockerManualComposition)
	}},
	{requirement: "dockerd@1/manual-storage-private-and-persistent", capability: capDockerd, needs: []string{capLifecycle}, run: func(ctx context.Context, e *Env) []report.Finding {
		return dockerStoreKept(ctx, e, dockerManualComposition, dockerManualToken)
	}},
	{requirement: "dockerd@1/manual-engine-not-started", capability: capDockerd, run: dockerManualNotStarted},
}

func init() {
	checks = append(checks, dockerChecks...)
}

// dockerSandbox creates a sandbox granting the engine. The page lets a
// host refuse the request, so a refusal skips the check rather than
// failing it.
func dockerSandbox(ctx context.Context, e *Env, kits []string) (string, func(), []report.Finding) {
	return dockerSandboxWith(ctx, e, kits, adapter.CreateOptions{})
}

func dockerSandboxWith(ctx context.Context, e *Env, kits []string, opts adapter.CreateOptions) (string, func(), []report.Finding) {
	id, cleanup, err := e.sandboxWith(ctx, kits, opts)
	var refused *adapter.RefusedError
	if errors.As(err, &refused) {
		return "", cleanup, []report.Finding{report.Skipf("runtime refused the engine request: %s", refused.Detail)}
	}
	if err != nil {
		return "", cleanup, []report.Finding{report.Failf("create: %v", err)}
	}
	return id, cleanup, nil
}

// Composed on the unlabelled workload, so a runtime that elevates for the
// start-docker label rather than for the request does not pass.
func dockerElevation(ctx context.Context, e *Env) []report.Finding {
	return dockerElevated(ctx, e, dockerComposition)
}

func dockerElevated(ctx context.Context, e *Env, kits []string) []report.Finding {
	id, cleanup, f := dockerSandbox(ctx, e, kits)
	defer cleanup()
	if f != nil {
		return f
	}
	got, finding := execOutput(ctx, e, id, "kit-tck-privileged")
	if finding != nil {
		return []report.Finding{*finding}
	}
	if got = strings.TrimSpace(got); got != "yes" {
		return []report.Finding{report.Failf("the kit requested the engine; the sandbox reports elevation %q", got)}
	}
	return nil
}

func dockerRecord(ctx context.Context, e *Env, id, path string) (string, []report.Finding) {
	got, f := execOutput(ctx, e, id, "cat", path)
	if f != nil {
		return "", []report.Finding{*f}
	}
	got = strings.TrimSpace(got)
	if got == "" {
		return "", []report.Finding{report.Failf("%s is empty", path)}
	}
	return got, nil
}

func dockerStorage(ctx context.Context, e *Env) []report.Finding {
	return dockerStoreKept(ctx, e, dockerComposition, dockerStoreToken)
}

// The token at path is written by the composition only when absent and
// read from the store itself, so a lost store reads as a new token.
// Recreation is what tells the store from the writable layer, and the
// scratch marker shows the layer really went. Privacy is judged by a
// second sandbox, which finds an empty store only if the first one's is
// not shared.
func dockerStoreKept(ctx context.Context, e *Env, kits []string, path string) []report.Finding {
	id, cleanup, f := dockerSandbox(ctx, e, kits)
	defer cleanup()
	if f != nil {
		return f
	}
	token, f := dockerRecord(ctx, e, id, path)
	if f != nil {
		return f
	}
	if err := e.Adapter.Stop(ctx, id); err != nil {
		return []report.Finding{report.Failf("stop: %v", err)}
	}
	if err := e.Adapter.Start(ctx, id); err != nil {
		return []report.Finding{report.Failf("start: %v", err)}
	}
	got, f := dockerRecord(ctx, e, id, path)
	if f != nil {
		return f
	}
	if got != token {
		return []report.Finding{report.Failf("the engine store did not survive stop and start: it held %q, then %q", token, got)}
	}

	if _, f := execOutput(ctx, e, id, "kit-tck-docker", "mark"); f != nil {
		return []report.Finding{*f}
	}
	if err := e.Adapter.Recreate(ctx, id); err != nil {
		return []report.Finding{report.Failf("recreate: %v", err)}
	}
	marked, err := e.Adapter.Exec(ctx, id, "kit-tck-docker", "marked")
	if err != nil {
		return []report.Finding{report.Failf("probe scratch: %v", err)}
	}
	if marked.ExitCode == 0 {
		return []report.Finding{report.Failf("the writable layer survived recreate, so the engine store's survival is unattributable")}
	}
	got, f = dockerRecord(ctx, e, id, path)
	if f != nil {
		return f
	}
	if got != token {
		return []report.Finding{report.Failf("the engine store did not survive recreation: it held %q, then %q", token, got)}
	}

	otherID, otherCleanup, f := dockerSandbox(ctx, e, kits)
	defer otherCleanup()
	if f != nil {
		return f
	}
	other, f := dockerRecord(ctx, e, otherID, path)
	if f != nil {
		return f
	}
	if other == token {
		return []report.Finding{report.Failf("a second sandbox found the first sandbox's store")}
	}
	return nil
}

// A sandbox created under a removed one's name must find a new store, as
// volume@1's removal check judges its storage: a store keyed by the alias
// would survive the removal.
func dockerRemoval(ctx context.Context, e *Env) []report.Finding {
	opts := adapter.CreateOptions{Name: "kit-tck-dockerd-" + rand.Text()}
	id, cleanup, f := dockerSandboxWith(ctx, e, dockerComposition, opts)
	remove := sync.OnceFunc(cleanup)
	defer remove()
	if f != nil {
		return f
	}
	token, f := dockerRecord(ctx, e, id, dockerStoreToken)
	if f != nil {
		return f
	}
	remove()
	fresh, removeFresh, f := dockerSandboxWith(ctx, e, dockerComposition, opts)
	defer removeFresh()
	if f != nil {
		return f
	}
	got, f := dockerRecord(ctx, e, fresh, dockerStoreToken)
	if f != nil {
		return f
	}
	if got == token {
		return []report.Finding{report.Failf("a sandbox created under a removed sandbox's name found its engine store %q", token)}
	}
	return nil
}

// Another Kit's volume at the engine store is refused at create, as
// host-mount@1 judges a volume at a mount's path.
func dockerStoreOwned(ctx context.Context, e *Env) []report.Finding {
	_, remove, err := e.sandbox(ctx, []string{fixtureDockerWorkload, fixtureDockerStoreVolume, fixtureDocker}, nil)
	remove()
	var refused *adapter.RefusedError
	if !errors.As(err, &refused) {
		return []report.Finding{report.Failf("a volume at /var/lib/docker beside dockerd@1 must be refused: %v", err)}
	}
	return nil
}

// A flag for any directive daemon.json can set is one a real engine
// refuses once the image's file sets it too, and a config file elsewhere
// is one that never reads the image's.
func dockerDaemonJSON(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, f := dockerSandbox(ctx, e, dockerComposition)
	defer cleanup()
	if f != nil {
		return f
	}
	raw, f := dockerRecord(ctx, e, id, dockerArgvRecord)
	if f != nil {
		return f
	}
	var argv []string
	if err := json.Unmarshal([]byte(raw), &argv); err != nil {
		return []report.Finding{report.Failf("the engine's recorded arguments %q: %v", raw, err)}
	}
	if why := daemonJSONViolation(argv); why != "" {
		return []report.Finding{report.Failf("%s", why)}
	}
	return nil
}

// daemonFileFlags are dockerd flags whose directive has a daemon.json
// key, as dockerd's reference documents the file. Short forms take their
// value joined as well as separate.
var (
	daemonFileFlags = map[string]bool{
		"--group": true, "--host": true, "--data-root": true,
		"--storage-driver": true, "--storage-opt": true,
		"--log-level": true, "--log-driver": true, "--log-opt": true,
		"--iptables": true, "--ip6tables": true, "--bridge": true, "--bip": true,
		"--default-address-pool": true, "--dns": true, "--dns-search": true, "--dns-opt": true,
		"--insecure-registry": true, "--registry-mirror": true, "--mtu": true,
		"--userns-remap": true, "--live-restore": true, "--features": true,
		"--exec-opt": true, "--default-runtime": true, "--containerd": true,
		"--tls": true, "--tlsverify": true, "--tlscacert": true, "--tlscert": true, "--tlskey": true,
		"--debug": true, "--experimental": true, "--pidfile": true,
	}
	daemonFileShortFlags = []string{"-G", "-H", "-b", "-s", "-l", "-D", "-p"}
)

// daemonJSONViolation names the first argument that sets a directive
// daemon.json can also set, or points dockerd at another config file.
// Pointing --config-file at the image's own file is still reading it.
func daemonJSONViolation(argv []string) string {
	for i, arg := range argv {
		flag, value, joined := strings.Cut(arg, "=")
		if !strings.HasPrefix(arg, "--") {
			for _, short := range daemonFileShortFlags {
				if strings.HasPrefix(arg, short) {
					return fmt.Sprintf("the runtime passed dockerd %q, a directive daemon.json can also set", arg)
				}
			}
		}
		if daemonFileFlags[flag] {
			return fmt.Sprintf("the runtime passed dockerd %q, a directive daemon.json can also set", arg)
		}
		if flag == "--config-file" {
			if !joined && i+1 < len(argv) {
				value = argv[i+1]
			}
			if path.Clean(value) != dockerDaemonConfig {
				return fmt.Sprintf("the runtime passed dockerd --config-file %q, so the image's %s is not the one it reads", value, dockerDaemonConfig)
			}
		}
	}
	return ""
}

// Each boot's hook must append the id of an engine started that boot.
// The second boot is judged against the first, so an engine left over
// from before, or one started only once, cannot pass for this boot's.
func dockerBeforeStartup(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, f := dockerSandbox(ctx, e, dockerComposition)
	defer cleanup()
	if f != nil {
		return f
	}
	var boots []string
	for boot := 1; boot <= 2; boot++ {
		if boot == 2 {
			if err := e.Adapter.Stop(ctx, id); err != nil {
				return []report.Finding{report.Failf("stop: %v", err)}
			}
			if err := e.Adapter.Start(ctx, id); err != nil {
				return []report.Finding{report.Failf("start: %v", err)}
			}
		}
		current, f := dockerRecord(ctx, e, id, dockerBootRecord)
		if f != nil {
			return f
		}
		seen, f := dockerRecord(ctx, e, id, dockerStartupRecord)
		if f != nil {
			return f
		}
		if len(boots) > 0 && current == boots[0] {
			return []report.Finding{report.Failf("no engine started on boot %d; the one recorded is boot 1's", boot)}
		}
		lines := strings.Split(seen, "\n")
		if len(lines) != boot {
			return []report.Finding{report.Failf("after boot %d the startup hook recorded %d boots: %q", boot, len(lines), seen)}
		}
		if hook := strings.TrimSpace(lines[boot-1]); hook != current {
			return []report.Finding{report.Failf("boot %d's startup hook saw engine %q; the engine started that boot is %q", boot, hook, current)}
		}
		boots = append(boots, current)
	}
	return nil
}

// The workload ships dockerd on PATH and declares nothing, so any engine
// record is an engine nobody asked for.
func dockerAbsentWithoutGrant(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{fixtureDockerWorkload}, nil)
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer cleanup()
	return dockerNeverStarted(ctx, e, id, "for a composition that did not request dockerd@1")
}

// Nothing in this composition starts the workload's stand-in engine, so
// a record is one the runtime started against autoStart false.
func dockerManualNotStarted(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, f := dockerSandbox(ctx, e, dockerManualComposition)
	defer cleanup()
	if f != nil {
		return f
	}
	return dockerNeverStarted(ctx, e, id, "although the Kit declared autoStart false")
}

// Probed again after a stop and start, so an engine started later in
// the background is caught too.
func dockerNeverStarted(ctx context.Context, e *Env, id, why string) []report.Finding {
	for boot := 1; boot <= 2; boot++ {
		if boot == 2 {
			if err := e.Adapter.Stop(ctx, id); err != nil {
				return []report.Finding{report.Failf("stop: %v", err)}
			}
			if err := e.Adapter.Start(ctx, id); err != nil {
				return []report.Finding{report.Failf("start: %v", err)}
			}
		}
		started, err := e.Adapter.Exec(ctx, id, "kit-tck-docker", "started")
		if err != nil {
			return []report.Finding{report.Failf("probe for an engine: %v", err)}
		}
		if started.ExitCode == 0 {
			return []report.Finding{report.Failf("by boot %d an engine had started %s", boot, why)}
		}
	}
	return nil
}
