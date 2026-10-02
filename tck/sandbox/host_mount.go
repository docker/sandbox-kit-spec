package sandbox

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const capHostMount = spec.CapabilityHostMount

// Fresh destinations prevent a previous run's cache from satisfying a
// persistence check and bound cleanup to directories this run requested.
func hostMountScope(ctx context.Context, e *Env, kits ...string) (string, func()) {
	path := "/var/tmp/kit-tck-host-" + rand.Text()
	return path, func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		for _, kit := range kits {
			mounts, err := e.Adapter.HostMounts(cleanupCtx, e.Fixtures(kit))
			if err != nil {
				e.hostMountLeaks = append(e.hostMountLeaks, fmt.Sprintf("%s at %s: %v", kit, path, err))
				continue
			}
			for _, mount := range mounts {
				if mount.Path == path {
					if err := e.Adapter.RemoveHostMount(cleanupCtx, mount.ID); err != nil {
						e.hostMountLeaks = append(e.hostMountLeaks, fmt.Sprintf("%s: %v", mount.ID, err))
					}
				}
			}
		}
	}
}

func hostSandbox(ctx context.Context, e *Env, kit, path string) (string, func(), error) {
	id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, kit}, map[string]string{"mount_path": path})
	return id, sync.OnceFunc(cleanup), err
}

func hostProbe(ctx context.Context, e *Env, id, operation, path, name, value, want string) []report.Finding {
	res, err := e.Adapter.Exec(ctx, id, "kit-tck-host-mount", operation, path, name, value)
	if err != nil || res.ExitCode != 0 || res.Stdout != want {
		return []report.Finding{report.Failf("%s at %s: exit %d, stdout %q, stderr %q, error %v; want %q", operation, path, res.ExitCode, res.Stdout, res.Stderr, err, want)}
	}
	return nil
}

func hostMountRecord(ctx context.Context, e *Env, kit, path string) (*adapter.HostMount, error) {
	mounts, err := e.Adapter.HostMounts(ctx, e.Fixtures(kit))
	if err != nil {
		return nil, err
	}
	for _, mount := range mounts {
		if mount.Path == path {
			return &mount, nil
		}
	}
	return nil, fmt.Errorf("no host directory listed for %s at %s", kit, path)
}

func hostMountHooks(ctx context.Context, e *Env) []report.Finding {
	path, cleanup := hostMountScope(ctx, e, "host-mount-hooks")
	defer cleanup()
	id, remove, err := hostSandbox(ctx, e, "host-mount-hooks", path)
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer remove()
	for _, name := range []string{"install", "startup"} {
		if findings := hostProbe(ctx, e, id, "read", path, name, "", "mounted"); len(findings) > 0 {
			return findings
		}
	}
	mount, err := hostMountRecord(ctx, e, "host-mount-hooks", path)
	if err != nil {
		return []report.Finding{report.Failf("find hook directory: %v", err)}
	}
	// A hook writing an ordinary image directory can look successful
	// inside the sandbox. Observing both writes on the host distinguishes
	// that from storage mounted before install and startup.
	for _, name := range []string{"install", "startup"} {
		res, err := e.Adapter.ReadHostMount(ctx, mount.ID, name)
		if err != nil || res.ExitCode != 0 || res.Stdout != "mounted" {
			return []report.Finding{report.Failf("%s hook did not write to host storage: %+v, %v", name, res, err)}
		}
	}
	return nil
}

func hostMountShared(ctx context.Context, e *Env) []report.Finding {
	path, cleanup := hostMountScope(ctx, e, "host-mount")
	defer cleanup()
	first, removeFirst, err := hostSandbox(ctx, e, "host-mount", path)
	if err != nil {
		return []report.Finding{report.Failf("first create: %v", err)}
	}
	defer removeFirst()
	second, removeSecond, err := hostSandbox(ctx, e, "host-mount", path)
	if err != nil {
		return []report.Finding{report.Failf("concurrent create: %v", err)}
	}
	defer removeSecond()
	for _, probe := range []struct{ id, operation, name, value, want string }{
		{first, "write", "first", "from-first", ""},
		{second, "read", "first", "", "from-first"},
		{second, "write", "second", "from-second", ""},
		{first, "read", "second", "", "from-second"},
	} {
		if findings := hostProbe(ctx, e, probe.id, probe.operation, path, probe.name, probe.value, probe.want); len(findings) > 0 {
			return findings
		}
	}
	return nil
}

func hostMountSurvivesRemoval(ctx context.Context, e *Env) []report.Finding {
	path, cleanup := hostMountScope(ctx, e, "host-mount")
	defer cleanup()
	first, removeFirst, err := hostSandbox(ctx, e, "host-mount", path)
	if err != nil {
		return []report.Finding{report.Failf("first create: %v", err)}
	}
	defer removeFirst()
	if findings := hostProbe(ctx, e, first, "write", path, "marker", "retained", ""); len(findings) > 0 {
		return findings
	}
	removeFirst()
	second, removeSecond, err := hostSandbox(ctx, e, "host-mount", path)
	if err != nil {
		return []report.Finding{report.Failf("create after last sandbox removed: %v", err)}
	}
	defer removeSecond()
	return hostProbe(ctx, e, second, "read", path, "marker", "", "retained")
}

func hostMountIsolated(ctx context.Context, e *Env) []report.Finding {
	path, cleanup := hostMountScope(ctx, e, "host-mount", "host-mount-other")
	defer cleanup()
	first, removeFirst, err := hostSandbox(ctx, e, "host-mount", path)
	if err != nil {
		return []report.Finding{report.Failf("first Kit: %v", err)}
	}
	defer removeFirst()
	if findings := hostProbe(ctx, e, first, "write", path, "marker", "private-to-kit", ""); len(findings) > 0 {
		return findings
	}
	removeFirst()
	// Both fixtures carry the same display label and source attribution;
	// their resolved Kit references supply distinct identities.
	other, removeOther, err := hostSandbox(ctx, e, "host-mount-other", path)
	if err != nil {
		return []report.Finding{report.Failf("second Kit: %v", err)}
	}
	defer removeOther()
	return hostProbe(ctx, e, other, "absent", path, "marker", "", "")
}

func hostMountListedAndRemovable(ctx context.Context, e *Env) []report.Finding {
	path, cleanup := hostMountScope(ctx, e, "host-mount")
	defer cleanup()
	id, remove, err := hostSandbox(ctx, e, "host-mount", path)
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer remove()
	if findings := hostProbe(ctx, e, id, "write", path, "marker", "host-visible", ""); len(findings) > 0 {
		return findings
	}
	remove()
	mount, err := hostMountRecord(ctx, e, "host-mount", path)
	if err != nil {
		return []report.Finding{report.Failf("list retained directory: %v", err)}
	}
	res, err := e.Adapter.ReadHostMount(ctx, mount.ID, "marker")
	if err != nil || res.ExitCode != 0 || res.Stdout != "host-visible" {
		return []report.Finding{report.Failf("sandbox writes not visible on host: %+v, %v", res, err)}
	}
	if err := e.Adapter.RemoveHostMount(ctx, mount.ID); err != nil {
		return []report.Finding{report.Failf("remove retained directory: %v", err)}
	}
	if remaining, err := e.Adapter.HostMounts(ctx, e.Fixtures("host-mount")); err != nil {
		return []report.Finding{report.Failf("list after removal: %v", err)}
	} else {
		for _, entry := range remaining {
			if entry.Path == path {
				return []report.Finding{report.Failf("removed directory still listed: %+v", entry)}
			}
		}
	}
	fresh, removeFresh, err := hostSandbox(ctx, e, "host-mount", path)
	if err != nil {
		return []report.Finding{report.Failf("create after directory removal: %v", err)}
	}
	defer removeFresh()
	return hostProbe(ctx, e, fresh, "absent", path, "marker", "", "")
}

func hostMountRoot(operation, value, want string) func(context.Context, *Env) []report.Finding {
	return func(ctx context.Context, e *Env) []report.Finding {
		path, cleanup := hostMountScope(ctx, e, "host-mount")
		defer cleanup()
		id, remove, err := hostSandbox(ctx, e, "host-mount", path)
		if err != nil {
			return []report.Finding{report.Failf("create: %v", err)}
		}
		defer remove()
		if findings := hostProbe(ctx, e, id, operation, path, "probe", value, want); len(findings) > 0 {
			return findings
		}
		if operation != "mode" {
			return nil
		}
		if findings := hostProbe(ctx, e, id, "set-mode", path, "", "750", ""); len(findings) > 0 {
			return findings
		}
		remove()
		fresh, removeFresh, err := hostSandbox(ctx, e, "host-mount", path)
		if err != nil {
			return []report.Finding{report.Failf("reopen: %v", err)}
		}
		defer removeFresh()
		return hostProbe(ctx, e, fresh, "mode", path, "", "", "750\n")
	}
}

func hostMountConflict(secondKit string) func(context.Context, *Env) []report.Finding {
	return func(ctx context.Context, e *Env) []report.Finding {
		path, cleanup := hostMountScope(ctx, e, "host-mount")
		defer cleanup()
		_, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "host-mount", secondKit}, map[string]string{"mount_path": path})
		remove()
		var refused *adapter.RefusedError
		if !errors.As(err, &refused) {
			return []report.Finding{report.Failf("same-path storage composition must refuse: %v", err)}
		}
		return nil
	}
}

func hostMountSurface(ctx context.Context, e *Env) []report.Finding {
	path, cleanup := hostMountScope(ctx, e, "host-mount")
	defer cleanup()
	id, remove, err := hostSandbox(ctx, e, "host-mount", path)
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer remove()
	state, err := e.Adapter.Selection(ctx, id)
	if err != nil {
		return []report.Finding{report.Failf("read effective host-sharing surface: %v", err)}
	}
	if !slices.Equal(state.Surface.HostMountPaths, []string{path}) || len(state.Surface.StoragePaths) != 0 || len(state.Surface.Services) != 0 {
		return []report.Finding{report.Failf("host-sharing path is not listed separately: %+v", state.Surface)}
	}
	return nil
}

func hostMountUnavailable(ctx context.Context, e *Env) []report.Finding {
	if e.claims(capHostMount) {
		return []report.Finding{report.Skipf("runtime claims host-directory sharing")}
	}
	path := "/var/tmp/kit-tck-host-" + rand.Text()
	_, removeRequired, err := hostSandbox(ctx, e, "host-mount", path)
	removeRequired()
	var refused *adapter.RefusedError
	if !errors.As(err, &refused) {
		return []report.Finding{report.Failf("unclaimed required host mount must refuse: %v", err)}
	}
	id, remove, err := hostSandbox(ctx, e, "host-mount-optional", path)
	if err != nil {
		return []report.Finding{report.Failf("unclaimed optional host mount must start: %v", err)}
	}
	defer remove()
	state, err := e.Adapter.Selection(ctx, id)
	if err != nil {
		return []report.Finding{report.Failf("read skipped host mount: %v", err)}
	}
	if len(state.Surface.HostMountPaths) != 0 || len(state.Surface.StoragePaths) != 0 || len(state.Surface.Services) != 0 {
		return []report.Finding{report.Failf("unclaimed host mount leaked a grant: %+v", state.Surface)}
	}
	for _, record := range state.Selection.Skipped {
		if record.Source != nil && (record.Source.Kit == "host-mount-optional" || record.Source.Kit == e.Fixtures("host-mount-optional")) && record.Path == "capabilities[0]" && record.Source.Path == "capabilities[0]" && slices.Equal(record.Members, []string{"capabilities[0]"}) && slices.Equal(record.Rejected, []string{"capabilities[0]"}) {
			return nil
		}
	}
	return []report.Finding{report.Failf("unclaimed optional host mount has no complete skipped record: %+v", state.Selection)}
}

var hostMountChecks = []check{
	{requirement: "host-mount@1/mounted-before-hooks", capability: capHostMount, needs: []string{capLifecycle}, run: hostMountHooks},
	{requirement: "host-mount@1/shared-concurrently", capability: capHostMount, run: hostMountShared},
	{requirement: "host-mount@1/survives-sandbox-removal", capability: capHostMount, run: hostMountSurvivesRemoval},
	{requirement: "host-mount@1/isolated-by-kit", capability: capHostMount, run: hostMountIsolated},
	{requirement: "host-mount@1/listed-and-removable", capability: capHostMount, run: hostMountListedAndRemovable},
	{requirement: "host-mount@1/agent-writable-root", capability: capHostMount, run: hostMountRoot("writable", "written-as-agent", "1000\n")},
	{requirement: "host-mount@1/initial-mode-applied", capability: capHostMount, run: hostMountRoot("mode", "", "700\n")},
	{requirement: "host-mount@1/no-silent-merge", capability: capHostMount, run: hostMountConflict("host-mount-other")},
	{requirement: "host-mount@1/host-volume-conflict", capability: capHostMount, needs: []string{capVolume}, run: hostMountConflict("host-mount-volume")},
	{requirement: "host-mount@1/separate-permission-surface", capability: capHostMount, run: hostMountSurface},
	{requirement: "host-mount@1/unadvertised-is-unmet", run: hostMountUnavailable},
}
