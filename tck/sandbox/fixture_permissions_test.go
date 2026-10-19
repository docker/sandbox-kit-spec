package sandbox

import (
	"archive/tar"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Descriptor validation cannot detect permissions on generated parent directories.
func TestPublishedVolumeFixtureDirectoryPermissions(t *testing.T) {
	if os.Getenv("KIT_TCK_TEST_BUILDKIT") != "1" {
		t.Skip("set KIT_TCK_TEST_BUILDKIT=1 to build the scratch fixture with Docker")
	}
	directory := filepath.Join(FixtureDir, "volume-state-published")
	archive := filepath.Join(t.TempDir(), "rootfs.tar")
	command := exec.CommandContext(t.Context(), "docker", "buildx", "build", "--provenance=false", "--output", "type=tar,dest="+archive, "-f", filepath.Join(directory, "volume-state-published.dockerfile"), directory)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	file, err := os.Open(archive)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	reader := tar.NewReader(file)
	seen := map[string]bool{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		name := filepath.Clean(header.Name)
		switch name {
		case "usr", "usr/share":
			require.Equal(t, byte(tar.TypeDir), header.Typeflag)
			require.EqualValues(t, 0o755, header.Mode&0o7777, "parent %s must remain traversable when composed over a workload", name)
			seen[name] = true
		case "usr/share/kit-tck-volume-published":
			require.Equal(t, byte(tar.TypeReg), header.Typeflag)
			require.Zero(t, header.Uid)
			require.Zero(t, header.Mode&0o022, "agent must not be able to overwrite the root-owned marker")
			body, err := io.ReadAll(reader)
			require.NoError(t, err)
			require.Equal(t, "published\n", string(body))
			seen[name] = true
		}
	}
	require.Len(t, seen, 3)
}
