package sandbox

import (
	"context"

	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

func hostMountPathsIsolated(ctx context.Context, e *Env) []report.Finding {
	scope := hostMountScope(ctx, e, "host-mount")
	defer scope.cleanup()
	first, removeFirst, err := scope.create("host-mount", nil)
	if err != nil {
		return []report.Finding{report.Failf("first destination: %v", err)}
	}
	defer removeFirst()
	otherPath := scope.path + "/other"
	second, removeSecond, err := scope.create("host-mount", map[string]string{"mount_path": otherPath})
	if err != nil {
		return []report.Finding{report.Failf("second destination: %v", err)}
	}
	defer removeSecond()
	for _, probe := range []struct{ id, operation, path, value, want string }{
		{first, "write", scope.path, "first-directory", ""},
		{second, "absent", otherPath, "", ""},
		{second, "write", otherPath, "second-directory", ""},
		{first, "read", scope.path, "", "first-directory"},
		{second, "read", otherPath, "", "second-directory"},
	} {
		if findings := hostProbe(ctx, e, probe.id, probe.operation, probe.path, "marker", probe.value, probe.want); len(findings) > 0 {
			return findings
		}
	}
	return nil
}

func hostMountSurvivesKitUpdate(ctx context.Context, e *Env) []report.Finding {
	const firstKit, updatedKit = "host-mount-version-v1", "host-mount-version-v2"
	scope := hostMountScope(ctx, e, firstKit, updatedKit)
	defer scope.cleanup()
	first, removeFirst, err := scope.create(firstKit, nil)
	if err != nil {
		return []report.Finding{report.Failf("first Kit version: %v", err)}
	}
	defer removeFirst()
	if findings := hostProbe(ctx, e, first, "write", scope.path, "marker", "before-update", ""); len(findings) > 0 {
		return findings
	}
	removeFirst()
	updated, removeUpdated, err := scope.create(updatedKit, nil)
	if err != nil {
		return []report.Finding{report.Failf("updated Kit version: %v", err)}
	}
	defer removeUpdated()
	if findings := hostProbe(ctx, e, updated, "read", scope.path, "marker", "", "before-update"); len(findings) > 0 {
		return findings
	}
	if findings := hostProbe(ctx, e, updated, "write", scope.path, "updated", "after-update", ""); len(findings) > 0 {
		return findings
	}
	removeUpdated()
	reopened, removeReopened, err := scope.create(firstKit, nil)
	if err != nil {
		return []report.Finding{report.Failf("reopen original Kit version: %v", err)}
	}
	defer removeReopened()
	return hostProbe(ctx, e, reopened, "read", scope.path, "updated", "", "after-update")
}
