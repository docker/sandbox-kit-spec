package sandbox

import (
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"sync"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const (
	volumePath        = "/var/tmp/kit-tck-volume"
	volumeOtherPath   = volumePath + "-other"
	volumeControlPath = volumePath + "-control"
)

func volumeAccess(ctx context.Context, e *Env, id, path, access string) []report.Finding {
	res, err := e.Adapter.Exec(ctx, id, "kit-tck-volume", access, path)
	if err != nil {
		return []report.Finding{report.Failf("initial volume access probe: %v", err)}
	}
	// Only the fixture's permission-denied status excuses an observation.
	// Missing roots and broken probes must not become conformance skips.
	if res.ExitCode == 3 {
		return []report.Finding{report.Skipf("cannot observe volume data at %s: initial root is not %s by the agent; mount-root accessibility is a SHOULD", path, access)}
	}
	if res.ExitCode != 0 {
		return []report.Finding{report.Failf("initial volume access probe at %s: exit %d: %s", path, res.ExitCode, res.Stderr)}
	}
	return nil
}

func volumeSeed(ctx context.Context, e *Env, id, path, name, value string) []report.Finding {
	if f := volumeAccess(ctx, e, id, path, "writable"); len(f) > 0 {
		return f
	}
	return volumeProbe(ctx, e, id, "write", path, name, value, "")
}

func volumeProbe(ctx context.Context, e *Env, id, operation, path, name, value, want string) []report.Finding {
	got, failure := execOutput(ctx, e, id, "kit-tck-volume", operation, path, name, value)
	if failure != nil {
		return []report.Finding{*failure}
	}
	if got != want {
		return []report.Finding{report.Failf("volume %s at %s: got %q, want %q", operation, path, got, want)}
	}
	return nil
}

func volumeMatching(ctx context.Context, e *Env) []report.Finding {
	// The second Kit uses a path alias, equivalent size/mode spellings,
	// distinct provenance, and an optional request. Storage settings
	// reconcile independently of metadata, and required wins.
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "volume-state", "volume-state-other"}, nil)
	if err != nil {
		return []report.Finding{report.Failf("matching volume composition: %v", err)}
	}
	defer remove()
	paths, err := e.Adapter.VolumePaths(ctx, id)
	if err != nil || !slices.Equal(sorted(paths), []string{volumePath, volumeOtherPath}) {
		return []report.Finding{report.Failf("matching requests must allocate one volume per cleaned path: %v (%v)", paths, err)}
	}
	return volumeProbe(ctx, e, id, "mounted", volumePath, "", "", "")
}

func volumeConflicts(ctx context.Context, e *Env) []report.Finding {
	// A functioning control rules out a runtime that refuses all creates.
	_, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "volume-state"}, nil)
	if err != nil {
		return []report.Finding{report.Failf("control create: %v", err)}
	}
	remove()
	for _, fixture := range []string{"volume-state-size", "volume-state-mode", "volume-state-unspecified", "volume-state-tmpfs"} {
		_, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "volume-state", fixture}, nil)
		remove()
		var refused *adapter.RefusedError
		if !errors.As(err, &refused) {
			return []report.Finding{report.Failf("conflicting %s composition must refuse: %v", fixture, err)}
		}
	}
	return nil
}

func volumeIdentity(ctx context.Context, e *Env) []report.Finding {
	a, removeA, err := e.sandbox(ctx, []string{fixtureWorkload, "volume-state"}, nil)
	if err != nil {
		return []report.Finding{report.Failf("first instance: %v", err)}
	}
	defer removeA()
	for _, path := range []string{volumePath, volumeOtherPath} {
		if f := volumeSeed(ctx, e, a, path, "marker", path); len(f) > 0 {
			return f
		}
	}
	b, removeB, err := e.sandbox(ctx, []string{fixtureWorkload, "volume-state"}, nil)
	if err != nil {
		return []report.Finding{report.Failf("second instance: %v", err)}
	}
	defer removeB()
	for _, path := range []string{volumePath, volumeOtherPath} {
		if f := volumeAccess(ctx, e, b, path, "readable"); len(f) > 0 {
			return f
		}
		if f := volumeProbe(ctx, e, b, "absent", path, "marker", "", ""); len(f) > 0 {
			return f
		}
		if f := volumeProbe(ctx, e, a, "read", path, "marker", "", path); len(f) > 0 {
			return f
		}
	}
	return nil
}

func volumeRecompose(ctx context.Context, e *Env) []report.Finding {
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "volume-state"}, nil)
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer remove()
	for _, path := range []string{volumePath, volumeOtherPath} {
		if f := volumeSeed(ctx, e, id, path, "marker", "retained"); len(f) > 0 {
			return f
		}
	}
	if f := volumeProbe(ctx, e, id, "write", volumeControlPath, "marker", "retained", ""); len(f) > 0 {
		return f
	}
	// A file created by the agent can be chmodded even when the runtime
	// owns the mount root. Root ownership and initial mode are SHOULDs.
	if f := volumeProbe(ctx, e, id, "set-mode", volumePath, "marker", "0600", ""); len(f) > 0 {
		return f
	}
	if err := e.Adapter.RecreateWith(ctx, id, []string{e.Fixtures("volume-workload"), e.Fixtures("volume-state-other")}, nil); err != nil {
		return []report.Finding{report.Failf("replace workload and declaring Kit: %v", err)}
	}
	for _, path := range []string{volumePath, volumeOtherPath} {
		if f := volumeProbe(ctx, e, id, "read", path, "marker", "", "retained"); len(f) > 0 {
			return f
		}
	}
	if f := volumeProbe(ctx, e, id, "absent", volumeControlPath, "marker", "", ""); len(f) > 0 {
		return f
	}
	// The publisher pin proves these are the flattened declarations of
	// matching requests, consumed under another Kit identity.
	if err := e.Adapter.RecreateWith(ctx, id, []string{e.Fixtures("volume-workload"), e.Fixtures("volume-state-published")}, nil); err != nil {
		return []report.Finding{report.Failf("replace loose mixins with published declarations: %v", err)}
	}
	for _, path := range []string{volumePath, volumeOtherPath} {
		if f := volumeProbe(ctx, e, id, "read", path, "marker", "", "retained"); len(f) > 0 {
			return f
		}
	}
	return volumeProbe(ctx, e, id, "mode", volumePath, "marker", "", "600\n")
}

func volumeRetained(ctx context.Context, e *Env) []report.Finding {
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "volume-state"}, nil)
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer remove()
	findings := volumeSeed(ctx, e, id, volumePath, "marker", "retained")
	seeded := len(findings) == 0
	if !seeded && findings[0].Severity != report.Skip {
		return findings
	}
	if err := e.Adapter.RecreateWith(ctx, id, []string{e.Fixtures(fixtureWorkload)}, nil); err != nil {
		return []report.Finding{report.Failf("remove declarations: %v", err)}
	}
	paths, err := e.Adapter.VolumePaths(ctx, id)
	if err != nil || !slices.Equal(sorted(paths), []string{volumePath, volumeOtherPath}) {
		return []report.Finding{report.Failf("retained destinations: %v (%v)", paths, err)}
	}
	for _, path := range paths {
		if f := volumeProbe(ctx, e, id, "unmounted", path, "", "", ""); len(f) > 0 {
			return f
		}
	}
	if f := volumeProbe(ctx, e, id, "write", volumePath, "unmounted", "container", ""); len(f) > 0 {
		return f
	}
	// A renamed destination starts fresh without inheriting the old path.
	newPath := volumePath + "-renamed"
	if err := e.Adapter.RecreateWith(ctx, id, []string{e.Fixtures(fixtureWorkload), e.Fixtures("volume-state-other")}, map[string]string{"volume_path": newPath}); err != nil {
		return []report.Finding{report.Failf("rename destination: %v", err)}
	}
	if f := volumeAccess(ctx, e, id, newPath, "readable"); len(f) > 0 {
		if f[0].Severity != report.Skip {
			return append(findings, f...)
		}
		findings = append(findings, f...)
	} else if f := volumeProbe(ctx, e, id, "empty", newPath, "", "", ""); len(f) > 0 {
		return append(findings, f...)
	}
	if err := e.Adapter.RecreateWith(ctx, id, nil, map[string]string{"volume_path": volumePath}); err != nil {
		return []report.Finding{report.Failf("restore destination: %v", err)}
	}
	if f := volumeProbe(ctx, e, id, "mounted", volumePath, "", "", ""); len(f) > 0 {
		return append(findings, f...)
	}
	if seeded {
		if f := volumeProbe(ctx, e, id, "read", volumePath, "marker", "", "retained"); len(f) > 0 {
			return append(findings, f...)
		}
	} else if f := volumeAccess(ctx, e, id, volumePath, "readable"); len(f) > 0 {
		return append(findings, f...)
	}
	return append(findings, volumeProbe(ctx, e, id, "absent", volumePath, "unmounted", "", "")...)
}

func sorted(paths []string) []string {
	paths = slices.Clone(paths)
	slices.Sort(paths)
	return paths
}

func volumeRemoval(ctx context.Context, e *Env) []report.Finding {
	opts := adapter.CreateOptions{Name: "kit-tck-volume-" + rand.Text()}
	kits := []string{fixtureWorkload, "volume-state"}
	id, cleanup, err := e.sandboxWith(ctx, kits, opts)
	if err != nil {
		return []report.Finding{report.Failf("named create: %v", err)}
	}
	remove := sync.OnceFunc(cleanup)
	defer remove()
	var findings []report.Finding
	for _, path := range []string{volumePath, volumeOtherPath} {
		if f := volumeSeed(ctx, e, id, path, "marker", "old-instance"); len(f) > 0 {
			if f[0].Severity != report.Skip {
				return append(findings, f...)
			}
			findings = append(findings, f...)
		}
	}
	if err := e.Adapter.RecreateWith(ctx, id, []string{e.Fixtures(fixtureWorkload)}, nil); err != nil {
		return []report.Finding{report.Failf("retain unmounted storage: %v", err)}
	}
	remove()
	paths, err := e.Adapter.VolumePaths(ctx, id)
	if err != nil || len(paths) != 0 {
		return append(findings, report.Failf("removed instance retains storage: %v (%v)", paths, err))
	}
	fresh, removeFresh, err := e.sandboxWith(ctx, kits, opts)
	if err != nil {
		return []report.Finding{report.Failf("new create with same alias: %v", err)}
	}
	defer removeFresh()
	for _, path := range []string{volumePath, volumeOtherPath} {
		if f := volumeAccess(ctx, e, fresh, path, "readable"); len(f) > 0 {
			if f[0].Severity != report.Skip {
				return append(findings, f...)
			}
			findings = append(findings, f...)
			continue
		}
		if f := volumeProbe(ctx, e, fresh, "empty", path, "", "", ""); len(f) > 0 {
			return append(findings, f...)
		}
	}
	return findings
}

func volumeRecreateConflicts(ctx context.Context, e *Env) []report.Finding {
	for _, override := range []map[string]string{
		{"volume_size": "2g"}, {"volume_size": ""},
		{"volume_mode": "0755"}, {"volume_mode": ""},
		nil, // Replacement with tmpfs changes the backing kind.
	} {
		id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "volume-state"}, nil)
		if err != nil {
			return []report.Finding{report.Failf("control create: %v", err)}
		}
		findings := func() []report.Finding {
			defer remove()
			if f := volumeSeed(ctx, e, id, volumePath, "marker", "unchanged"); len(f) > 0 {
				return f
			}
			if f := volumeProbe(ctx, e, id, "write", volumeControlPath, "marker", "unchanged", ""); len(f) > 0 {
				return f
			}
			for _, path := range []string{volumePath, volumeControlPath} {
				if f := volumeProbe(ctx, e, id, "set-mode", path, "marker", "0600", ""); len(f) > 0 {
					return f
				}
			}
			var kits []string
			if override == nil {
				kits = []string{e.Fixtures(fixtureWorkload), e.Fixtures("volume-state-tmpfs")}
			}
			err = e.Adapter.RecreateWith(ctx, id, kits, override)
			var refused *adapter.RefusedError
			if !errors.As(err, &refused) {
				return []report.Finding{report.Failf("incompatible recreation must refuse: %v", err)}
			}
			// Both markers must survive: volume retention alone cannot
			// prove refusal happened before the old container was replaced.
			for _, path := range []string{volumePath, volumeControlPath} {
				if f := volumeProbe(ctx, e, id, "read", path, "marker", "", "unchanged"); len(f) > 0 {
					return f
				}
				if f := volumeProbe(ctx, e, id, "mode", path, "marker", "", "600\n"); len(f) > 0 {
					return f
				}
			}
			if err := e.Adapter.Recreate(ctx, id); err != nil {
				return []report.Finding{report.Failf("refusal altered retained inputs: %v", err)}
			}
			if f := volumeProbe(ctx, e, id, "read", volumePath, "marker", "", "unchanged"); len(f) > 0 {
				return f
			}
			return volumeProbe(ctx, e, id, "mode", volumePath, "marker", "", "600\n")
		}()
		if len(findings) > 0 {
			return findings
		}
	}
	return volumeDormantRecreateConflicts(ctx, e)
}

func volumeDormantRecreateConflicts(ctx context.Context, e *Env) []report.Finding {
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "volume-state"}, nil)
	if err != nil {
		return []report.Finding{report.Failf("create retained storage: %v", err)}
	}
	defer remove()
	if f := volumeSeed(ctx, e, id, volumePath, "marker", "retained"); len(f) > 0 {
		return f
	}
	if f := volumeProbe(ctx, e, id, "set-mode", volumePath, "marker", "0600", ""); len(f) > 0 {
		return f
	}
	if err := e.Adapter.RecreateWith(ctx, id, []string{e.Fixtures(fixtureWorkload)}, nil); err != nil {
		return []report.Finding{report.Failf("undeclare storage: %v", err)}
	}
	if f := volumeProbe(ctx, e, id, "write", volumeControlPath, "marker", "unchanged", ""); len(f) > 0 {
		return f
	}
	for _, override := range []map[string]string{{"volume_size": "2g"}, {"volume_mode": "0755"}, nil} {
		fixture := "volume-state"
		if override == nil {
			fixture = "volume-state-tmpfs"
		}
		err := e.Adapter.RecreateWith(ctx, id, []string{e.Fixtures(fixtureWorkload), e.Fixtures(fixture)}, override)
		var refused *adapter.RefusedError
		if !errors.As(err, &refused) {
			return []report.Finding{report.Failf("incompatible dormant storage must refuse: %v", err)}
		}
		if f := volumeProbe(ctx, e, id, "read", volumeControlPath, "marker", "", "unchanged"); len(f) > 0 {
			return f
		}
		if f := volumeProbe(ctx, e, id, "unmounted", volumePath, "", "", ""); len(f) > 0 {
			return f
		}
	}
	// Refusal must keep the workload-only composition and its inputs;
	// only a compatible, explicit reintroduction may expose the data.
	if err := e.Adapter.Recreate(ctx, id); err != nil {
		return []report.Finding{report.Failf("refusal altered dormant composition: %v", err)}
	}
	if f := volumeProbe(ctx, e, id, "unmounted", volumePath, "", "", ""); len(f) > 0 {
		return f
	}
	if err := e.Adapter.RecreateWith(ctx, id, []string{e.Fixtures(fixtureWorkload), e.Fixtures("volume-state")}, nil); err != nil {
		return []report.Finding{report.Failf("compatible dormant reattachment: %v", err)}
	}
	if f := volumeProbe(ctx, e, id, "read", volumePath, "marker", "", "retained"); len(f) > 0 {
		return f
	}
	return volumeProbe(ctx, e, id, "mode", volumePath, "marker", "", "600\n")
}

func volumeEmpty(ctx context.Context, e *Env) []report.Finding {
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "volume-state"}, nil)
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	defer remove()
	if f := volumeAccess(ctx, e, id, volumePath, "readable"); len(f) > 0 {
		return f
	}
	return volumeProbe(ctx, e, id, "empty", volumePath, "", "", "")
}

func volumeTmpfs(ctx context.Context, e *Env) []report.Finding {
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "volume-state-tmpfs"}, nil)
	if err != nil {
		return []report.Finding{report.Failf("tmpfs create: %v", err)}
	}
	defer remove()
	for _, recreate := range []bool{false, true} {
		if f := volumeSeed(ctx, e, id, volumePath, "marker", "scratch"); len(f) > 0 {
			return f
		}
		if recreate {
			err = e.Adapter.Recreate(ctx, id)
		} else {
			err = e.Adapter.Stop(ctx, id)
			if err == nil {
				err = e.Adapter.Start(ctx, id)
			}
		}
		if err != nil {
			return []report.Finding{report.Failf("tmpfs restart/recreate: %v", err)}
		}
		// Cleared tmpfs is a fresh allocation, not retained block storage.
		// Initial permissions remain SHOULDs even on a replacement root.
		if f := volumeAccess(ctx, e, id, volumePath, "readable"); len(f) > 0 {
			return f
		}
		if f := volumeProbe(ctx, e, id, "absent", volumePath, "marker", "", ""); len(f) > 0 {
			return f
		}
	}
	return nil
}

func volumeHooks(ctx context.Context, e *Env) []report.Finding {
	id, remove, err := e.sandbox(ctx, []string{fixtureWorkload, "volume-state-hooks"}, nil)
	if err != nil {
		return []report.Finding{report.Failf("create with mount-observing hooks: %v", err)}
	}
	defer remove()
	if f := volumeAccess(ctx, e, id, volumePath, "writable"); len(f) > 0 {
		return f
	}
	for _, hook := range []string{"install", "startup"} {
		if f := volumeProbe(ctx, e, id, "read", volumePath, hook, "", "mounted"); len(f) > 0 {
			return f
		}
	}
	for _, recreate := range []bool{false, true} {
		if f := volumeProbe(ctx, e, id, "write", volumePath, "startup", "stale", ""); len(f) > 0 {
			return f
		}
		if recreate {
			err = e.Adapter.Recreate(ctx, id)
		} else if err = e.Adapter.Stop(ctx, id); err == nil {
			err = e.Adapter.Start(ctx, id)
		}
		if err != nil {
			return []report.Finding{report.Failf("restart/recreate with mount-observing hooks: %v", err)}
		}
		if f := volumeProbe(ctx, e, id, "read", volumePath, "startup", "", "mounted"); len(f) > 0 {
			return f
		}
	}
	return nil
}

var volumeChecks = []check{
	{requirement: "volume@1/matching-requests-merge", capability: capVolume, run: volumeMatching},
	{requirement: "volume@1/no-silent-merge", capability: capVolume, run: volumeConflicts},
	{requirement: "volume@1/instance-and-path-identity", capability: capVolume, run: volumeIdentity},
	{requirement: "volume@1/composition-independent", capability: capVolume, run: volumeRecompose},
	{requirement: "volume@1/undeclared-retained", capability: capVolume, run: volumeRetained},
	{requirement: "volume@1/removal-deletes-storage", capability: capVolume, run: volumeRemoval},
	{requirement: "volume@1/recreate-config-compatible", capability: capVolume, run: volumeRecreateConflicts},
	{requirement: "volume@1/initially-empty", capability: capVolume, run: volumeEmpty},
	{requirement: "volume@1/tmpfs-cleared", capability: capVolume, run: volumeTmpfs},
	{requirement: "volume@1/mounted-before-hooks", capability: capVolume, needs: []string{capLifecycle}, run: volumeHooks},
}
