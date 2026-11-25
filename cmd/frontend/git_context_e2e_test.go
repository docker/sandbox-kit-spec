package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	tckkit "github.com/docker/sandbox-kit-spec/v3/tck/kit"
)

// A remote Git build carries no local source session. This is the path
// used by docker/github-builder, where a successful checkout in the
// caller's job cannot compensate for a frontend that asks for locals.
func TestGitContextBuildsEndToEnd(t *testing.T) {
	if os.Getenv("KIT_E2E") == "" {
		t.Skip("set KIT_E2E=1 to run Git-context builds (needs Docker; task test:e2e)")
	}
	if out, err := exec.CommandContext(t.Context(), "docker", "info", "--format", "{{.ServerVersion}}").Output(); err != nil || len(out) == 0 {
		t.Skip("the docker daemon is not reachable")
	}

	e := &e2e{t: t, builder: dockerDriverBuilder(t)}
	frontend := e.buildFrontend()
	registry := e.startRegistry()
	repo := t.TempDir()
	root := filepath.Join(repo, "kit")
	write := func(name, body string) {
		t.Helper()
		filename := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
		require.NoError(t, os.WriteFile(filename, []byte(body), 0o644))
	}
	descriptor := `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/agent-context@1
    config:
      contentFile: ./notes.md
`
	recipe := "FROM scratch\nCOPY payload /payload\nCOPY . /snapshot/\n"
	write("conventional/demo.yaml", "# syntax="+frontend+"\n"+descriptor)
	write("conventional/demo.dockerfile", recipe)
	write("explicit/demo.yaml", "# syntax="+frontend+"\n"+descriptor+"dockerfile: recipes/custom.dockerfile\n")
	write("explicit/recipes/custom.dockerfile", recipe)
	write("inline/demo.yaml", "# syntax="+frontend+"\n"+descriptor+"build: |\n  "+strings.ReplaceAll(strings.TrimSuffix(recipe, "\n"), "\n", "\n  ")+"\n")
	write("comment/demo.dockerfile", "# syntax="+frontend+"\n# kit:\n#   "+strings.ReplaceAll(strings.TrimSuffix(descriptor, "\n"), "\n", "\n#   ")+"\n\n"+recipe)
	write("payload", "committed payload\n")
	write("notes.md", "Committed guidance.\n")
	write("ignored.txt", "Not part of COPY.\n")
	// Guidance is an explicit descriptor source, independent of COPY's
	// exclusions. The same fixture checks that these remain separate for
	// both local and Git contexts.
	write(".dockerignore", "notes.md\nignored.txt\n")
	e.run("git", "-C", repo, "init", "-b", "main")
	e.run("git", "-C", repo, "add", ".")
	e.run("git", "-C", repo, "-c", "user.name=Kit fixture", "-c", "user.email=kit-fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	commit := strings.TrimSpace(e.run("git", "-C", repo, "rev-parse", "HEAD"))
	gitURL := e.startGitServer(repo)
	// A Git build must use the committed bytes, even if the caller has
	// changed its working tree after selecting the source commit.
	write("payload", "uncommitted payload\n")
	gitContext := gitURL + "?ref=" + commit + "&checksum=" + commit + "&subdir=kit"
	// dockerfile.v0 currently leaves remote Git trees unfiltered while
	// local sessions apply .dockerignore. Compare with an ordinary recipe
	// so this test tracks BuildKit's behavior if that distinction changes.
	native := t.TempDir()
	e.run("docker", "buildx", "--builder", e.builder, "build", gitContext,
		"-f", "conventional/demo.dockerfile", "--output", "type=local,dest="+native)

	for _, tt := range []struct {
		name  string
		file  string
		local bool
	}{
		{name: "conventional", file: "conventional/demo.yaml"},
		{name: "explicit", file: "explicit/demo.yaml"},
		{name: "inline", file: "inline/demo.yaml"},
		{name: "comment", file: "comment/demo.dockerfile"},
		{name: "local-guidance", file: "explicit/demo.yaml", local: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := &e2e{t: t, builder: e.builder}
			context := gitContext
			file := tt.file
			payload := "committed payload\n"
			if tt.local {
				context = root
				file = filepath.Join(root, tt.file)
				payload = "uncommitted payload\n"
			}
			ref := registry + "/git-context:" + tt.name
			e.run("docker", "buildx", "--builder", e.builder, "build", context,
				"-f", file, "--push", "-t", ref,
				"--platform=linux/amd64,linux/arm64", "--provenance=mode=min")
			artifacts, err := tckkit.FromRegistryAll(t.Context(), ref)
			require.NoError(t, err)
			require.Len(t, artifacts, 2)
			for _, artifact := range artifacts {
				body, present, err := artifact.ReadFile(t.Context(), "/payload")
				require.NoError(t, err)
				require.True(t, present)
				require.Equal(t, payload, string(body))
				for _, excluded := range []string{"notes.md", "ignored.txt"} {
					_, present, err := artifact.ReadFile(t.Context(), "/snapshot/"+excluded)
					require.NoError(t, err)
					expected := false
					if !tt.local {
						_, err := os.Stat(filepath.Join(native, "snapshot", excluded))
						require.True(t, err == nil || os.IsNotExist(err), "%v", err)
						expected = err == nil
					}
					require.Equal(t, expected, present, "COPY must match native context filtering")
				}
				d, err := spec.Decode([]byte(artifact.Annotations()[spec.AnnotationDescriptor]))
				require.NoError(t, err)
				guidance, err := spec.AgentContextOf(spec.DeclaredCapabilities(d.Capabilities))
				require.NoError(t, err)
				require.NotNil(t, guidance)
				body, present, err = artifact.ReadFile(t.Context(), guidance.ContentFile)
				require.NoError(t, err)
				require.True(t, present)
				require.Equal(t, "Committed guidance.\n", string(body))
				report, err := tckkit.Run(t.Context(), artifact)
				require.NoError(t, err)
				require.NoError(t, report.Err(), "%s", report)
			}
		})
	}
}

// Serve Git in a container so the docker-driver builder can reach it on
// the same host-side network as the test registry, including Docker
// Desktop. The frontend's existing Go build image already carries git.
func (e *e2e) startGitServer(repo string) string {
	e.t.Helper()
	root := e.t.TempDir()
	e.run("git", "clone", "--bare", repo, filepath.Join(root, "kit.git"))
	name := fmt.Sprintf("kit-e2e-git-%d", time.Now().UnixNano())
	// docker cp can preserve the host UID. Trust only this temporary
	// repository so Git's ownership check does not reject upload-pack.
	e.run("docker", "create", "--name", name, "--user", "0:0", "-p", "0:9418",
		"--entrypoint", "git", "dhi.io/golang:1.27-dev", "-c", "safe.directory=/repos/kit.git",
		"daemon", "--export-all", "--base-path=/repos", "--listen=0.0.0.0", "/repos")
	e.t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	e.run("docker", "cp", root+"/.", name+":/repos")
	e.run("docker", "start", name)
	published := strings.Split(strings.TrimSpace(e.run("docker", "port", name, "9418/tcp")), "\n")[0]
	address := "localhost:" + published[strings.LastIndex(published, ":")+1:]
	deadline := time.Now().Add(30 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			_ = conn.Close()
			url := "git://" + address + "/kit.git"
			e.run("git", "ls-remote", url, "HEAD")
			return url
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("Git server at %s never came up: %v", address, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
