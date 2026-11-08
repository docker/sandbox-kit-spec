package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// VolumeStoragePaths observes retained block allocations by sandbox name
// in the adapter's owner scope, even without a live sandbox handle.
func (a *Adapter) VolumeStoragePaths(ctx context.Context, name string) ([]string, error) {
	res, err := a.run(ctx, "volume-storage", name)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("volume-storage: exit %d: %s", res.ExitCode, res.Stderr)
	}
	var paths []string
	if err := json.Unmarshal([]byte(res.Stdout), &paths); err != nil {
		return nil, fmt.Errorf("volume-storage: %w", err)
	}
	if paths == nil {
		return nil, fmt.Errorf("volume-storage: expected a JSON array")
	}
	seen := map[string]bool{}
	for _, destination := range paths {
		if !strings.HasPrefix(destination, "/") || path.Clean(destination) != destination || strings.ContainsRune(destination, '\x00') || seen[destination] {
			return nil, fmt.Errorf("volume-storage: invalid or duplicate destination %q", destination)
		}
		seen[destination] = true
	}
	return paths, nil
}

// RemoveVolumeStorage removes a name's retained block storage. An empty
// destination removes all paths; otherwise only that cleaned path is removed.
func (a *Adapter) RemoveVolumeStorage(ctx context.Context, name, destination string) error {
	argv := []string{"volume-rm", name}
	if destination != "" {
		argv = append(argv, destination)
	}
	return a.mustRun(ctx, argv...)
}
