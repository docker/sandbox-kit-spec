package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Only a DRM node answers DRM_IOCTL_VERSION, so a substitute character
// device at a render-node path names no driver and cannot pass for Venus.
func TestDRMDriverProbeRejectsNonDRMNodes(t *testing.T) {
	ordinary := filepath.Join(t.TempDir(), "ordinary")
	require.NoError(t, os.WriteFile(ordinary, []byte("not a device"), 0600))
	for _, path := range []string{filepath.Join(t.TempDir(), "missing"), ordinary, "/dev/null"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "python3", filepath.Join("testdata", "fixtures", "workload", "kit-tck-drm-driver.py"), path)
			output, err := cmd.CombinedOutput()
			var status *exec.ExitError
			require.ErrorAs(t, err, &status)
			require.Equal(t, 3, status.ExitCode(), "%s", output)
			require.Contains(t, string(output), "unavailable:"+path+":")
		})
	}
}
