package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// HostMount identifies one runtime-managed directory without making its
// opaque removal handle a Kit-supplied host path.
type HostMount struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	HostPath string `json:"hostPath"`
}

// HostMounts lists retained directories for a resolved Kit identity,
// including after its last sandbox was removed.
func (a *Adapter) HostMounts(ctx context.Context, kit string) ([]HostMount, error) {
	res, err := a.run(ctx, "host-mounts", kit)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("host-mounts: exit %d: %s", res.ExitCode, res.Stderr)
	}
	var mounts []HostMount
	if err := json.Unmarshal([]byte(res.Stdout), &mounts); err != nil {
		return nil, fmt.Errorf("host-mounts: %w", err)
	}
	if mounts == nil {
		return nil, fmt.Errorf("host-mounts: expected a JSON array")
	}
	ids, paths := map[string]bool{}, map[string]bool{}
	for _, mount := range mounts {
		if mount.ID == "" || mount.HostPath == "" || !strings.HasPrefix(mount.Path, "/") || mount.Path == "/" || path.Clean(mount.Path) != mount.Path || strings.ContainsRune(mount.Path, '\x00') {
			return nil, fmt.Errorf("host-mounts: incomplete directory record: %+v", mount)
		}
		if ids[mount.ID] || paths[mount.Path] {
			return nil, fmt.Errorf("host-mounts: duplicate directory record: %+v", mount)
		}
		ids[mount.ID], paths[mount.Path] = true, true
	}
	return mounts, nil
}

// ReadHostMount observes file bytes on the host, independently of sandbox exec.
func (a *Adapter) ReadHostMount(ctx context.Context, id, relativePath string) (Result, error) {
	return a.run(ctx, "host-mount-read", id, relativePath)
}

// RemoveHostMount removes only the directory identified by a listing handle.
func (a *Adapter) RemoveHostMount(ctx context.Context, id string) error {
	return a.mustRun(ctx, "host-mount-rm", id)
}
