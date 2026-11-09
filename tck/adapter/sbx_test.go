package adapter

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// Observe the real shell adapter at its CLI boundary without creating sandboxes.
const skillsStub = `#!/bin/sh
set -eu
if [ "${1:-}" = --app-name ]; then shift 2; fi
case "$1" in
skills) printf '{"store":"%s"}\n' "$STUB_STORE" ;;
create)
  shift
  mode=unset
  while [ $# -gt 0 ]; do
    if [ "$1" = --skills ]; then mode="$2"; shift; fi
    shift
  done
  contents=missing
  if [ -d "$STUB_STORE" ]; then
    contents=empty
    if [ -n "$(ls -A "$STUB_STORE")" ]; then contents=populated; fi
  fi
  printf '%s %s\n' "$mode" "$contents" >"$STUB_OBSERVED"
  if [ "${STUB_FAIL:-}" = yes ]; then echo 'create failed unexpectedly' >&2; exit 3; fi
  ;;
rm) ;;
ls) echo '[]' ;;
*) echo "unexpected stub call: $*" >&2; exit 1 ;;
esac
`

func skillsAdapter(t *testing.T) (*Adapter, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "sbx")
	require.NoError(t, os.WriteFile(stub, []byte(skillsStub), 0700))
	store, state, observed := filepath.Join(dir, "store"), filepath.Join(dir, "state"), filepath.Join(dir, "observed")
	a := New(filepath.Join("..", "adapters", "sbx"))
	a.Env = []string{
		"SBX=" + stub, "SBX_TCK_STATE=" + state, "SBX_TCK_APP_NAME=skills-probe",
		"SBX_TCK_CAPABILITIES=com.docker.sandbox/agent-skills@1", "SBX_TCK_SHARE_SKILLS_STORE=1",
		"KIT_TCK_SKILL_NAME=probe", "STUB_STORE=" + store, "STUB_OBSERVED=" + observed, "STUB_FAIL=",
	}
	return a, store, state, observed
}

func TestSbxSkillsStoreScenariosRestoreContent(t *testing.T) {
	for _, scenario := range []string{"missing", "empty"} {
		for _, existing := range []bool{false, true} {
			for _, failed := range []bool{false, true} {
				t.Run(scenario+map[bool]string{false: "/new", true: "/existing"}[existing]+map[bool]string{false: "/success", true: "/failure"}[failed], func(t *testing.T) {
					a, store, state, observed := skillsAdapter(t)
					if existing {
						require.NoError(t, os.MkdirAll(store, 0700))
						require.NoError(t, os.WriteFile(filepath.Join(store, ".existing"), []byte("preserve me"), 0600))
					}
					if failed {
						a.Env = append(a.Env, "STUB_FAIL=yes")
					}
					id, err := a.Create(t.Context(), []string{"workload"}, CreateOptions{SkillsHostStore: scenario})
					if failed {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
						require.NoError(t, a.Remove(t.Context(), id))
					}
					raw, err := os.ReadFile(observed)
					require.NoError(t, err)
					require.Equal(t, "readwrite "+scenario+"\n", string(raw), "sharing stays enabled and marker seeding is suppressed")
					if existing {
						raw, err = os.ReadFile(filepath.Join(store, ".existing"))
						require.NoError(t, err)
						require.Equal(t, "preserve me", string(raw))
					} else {
						require.NoDirExists(t, store)
					}
					require.NoFileExists(t, filepath.Join(state, "skills-store.path"))
					require.NoDirExists(t, filepath.Join(state, "skills-store.backup"))
					// The next ordinary create must receive the normal seeded store.
					a.Env = append(a.Env, "STUB_FAIL=")
					id, err = a.Create(t.Context(), []string{"workload"}, CreateOptions{})
					require.NoError(t, err)
					raw, err = os.ReadFile(observed)
					require.NoError(t, err)
					require.Equal(t, "readwrite populated\n", string(raw))
					require.FileExists(t, filepath.Join(store, "probe", "SKILL.md"))
					require.NoError(t, a.Remove(t.Context(), id))
				})
			}
		}
	}
}

func TestSbxSkillsStoreProbePreservesUnexpectedContent(t *testing.T) {
	a, store, state, _ := skillsAdapter(t)
	require.NoError(t, os.MkdirAll(store, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(store, "original"), []byte("original"), 0600))
	id, err := a.Create(t.Context(), []string{"workload"}, CreateOptions{SkillsHostStore: "empty"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(store, "new"), []byte("new"), 0600))
	err = a.Remove(t.Context(), id)
	require.ErrorContains(t, err, "probe store is not empty")
	require.FileExists(t, filepath.Join(store, "new"))
	require.FileExists(t, filepath.Join(state, "skills-store.backup", "original"))
	require.FileExists(t, filepath.Join(state, "skills-store.path"))
}

// Observe SSH_AUTH_SOCK at the real adapter's create boundary: the suite
// socket must be exported, an inherited host socket must not substitute
// when --ssh-agent is absent, and recreate must replay the original.
const sshStub = `#!/bin/sh
set -eu
if [ "${1:-}" = --app-name ]; then shift 2; fi
case "$1" in
create)
  if [ -n "${SSH_AUTH_SOCK+x}" ]; then
    printf 'set:%s\n' "$SSH_AUTH_SOCK" >>"$STUB_OBSERVED"
  else
    printf 'unset\n' >>"$STUB_OBSERVED"
  fi
  ;;
rm) ;;
ls) echo '[]' ;;
*) echo "unexpected stub call: $*" >&2; exit 1 ;;
esac
`

func sshAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "sbx")
	require.NoError(t, os.WriteFile(stub, []byte(sshStub), 0700))
	state, observed := filepath.Join(dir, "state"), filepath.Join(dir, "observed")
	a := New(filepath.Join("..", "adapters", "sbx"))
	a.Env = []string{
		"SBX=" + stub, "SBX_TCK_STATE=" + state, "SBX_TCK_APP_NAME=ssh-probe",
		"SBX_TCK_CAPABILITIES=com.docker.sandbox/ssh-agent@1",
		"STUB_OBSERVED=" + observed,
		// A host agent the adapter must not inherit when the suite withholds
		// --ssh-agent (§2.3).
		"SSH_AUTH_SOCK=" + filepath.Join(dir, "host-agent.sock"),
	}
	return a, observed
}

func TestSbxSSHAgentExportsOfferedSocket(t *testing.T) {
	a, observed := sshAdapter(t)
	socket := filepath.Join(t.TempDir(), "suite-agent.sock")
	id, err := a.Create(t.Context(), []string{"workload"}, CreateOptions{SSHAgent: socket})
	require.NoError(t, err)
	raw, err := os.ReadFile(observed)
	require.NoError(t, err)
	require.Equal(t, "set:"+socket+"\n", string(raw))
	require.NoError(t, a.Remove(t.Context(), id))
}

func TestSbxSSHAgentClearsInheritedSocketWhenAbsent(t *testing.T) {
	a, observed := sshAdapter(t)
	id, err := a.Create(t.Context(), []string{"workload"}, CreateOptions{})
	require.NoError(t, err)
	raw, err := os.ReadFile(observed)
	require.NoError(t, err)
	require.Equal(t, "unset\n", string(raw), "host SSH_AUTH_SOCK must not reach sbx without --ssh-agent")
	require.NoError(t, a.Remove(t.Context(), id))
}

func TestSbxSSHAgentReplaysBindingOnRecreate(t *testing.T) {
	a, observed := sshAdapter(t)
	socket := filepath.Join(t.TempDir(), "suite-agent.sock")
	id, err := a.Create(t.Context(), []string{"workload"}, CreateOptions{SSHAgent: socket})
	require.NoError(t, err)
	// Point the process env at a different socket; recreate must still use
	// the remembered suite agent, not this one.
	a.Env = append(a.Env, "SSH_AUTH_SOCK="+filepath.Join(t.TempDir(), "other-agent.sock"))
	require.NoError(t, a.Recreate(t.Context(), id))
	raw, err := os.ReadFile(observed)
	require.NoError(t, err)
	require.Equal(t, "set:"+socket+"\nset:"+socket+"\n", string(raw))
	require.NoError(t, a.Remove(t.Context(), id))
}

func TestSbxVolumeClaimOverrideRefusesBeforeChangingSandbox(t *testing.T) {
	a, observed := sshAdapter(t)
	dir := t.TempDir()
	removed := filepath.Join(dir, "removed")
	stub := filepath.Join(dir, "sbx")
	script := strings.Replace(sshStub, "rm) ;;", "rm) touch \"$STUB_REMOVED\" ;;", 1)
	require.NoError(t, os.WriteFile(stub, []byte(script), 0700))
	a.Env = append(a.Env, "SBX="+stub, "STUB_REMOVED="+removed, "SBX_TCK_CAPABILITIES=com.docker.sandbox/volume@1")
	id, err := a.Create(t.Context(), []string{"workload"}, CreateOptions{Name: "kit-tck-volume-alias"})
	require.NoError(t, err)
	require.Equal(t, "kit-tck-volume-alias", id)
	err = a.Recreate(t.Context(), id)
	require.ErrorContains(t, err, "does not implement name-scoped volume lifetime")
	require.NoFileExists(t, removed, "a failed recreation must not destroy the instance")
	raw, err := os.ReadFile(observed)
	require.NoError(t, err)
	require.Equal(t, "unset\n", string(raw), "no replacement create was attempted")
	_, err = a.VolumePaths(t.Context(), id)
	require.ErrorContains(t, err, "does not expose retained name-scoped volume storage")
	require.NoError(t, a.Remove(t.Context(), id))
	require.FileExists(t, removed, "the stub observes a real removal")
}

// Record create argv so selection/policy/identity adapter paths are
// observable without a real daemon.
const selectionStub = `#!/bin/sh
set -eu
if [ "${1:-}" = --app-name ]; then shift 2; fi
case "$1" in
create)
  shift
  printf '%s\n' "$*" >>"$STUB_OBSERVED"
  ;;
rm | stop) ;;
run) ;;
ls) echo '[]' ;;
*) echo "unexpected stub call: $*" >&2; exit 1 ;;
esac
`

func selectionAdapter(t *testing.T, claims string) (*Adapter, string) {
	t.Helper()
	dir := t.TempDir()
	stub := filepath.Join(dir, "sbx")
	require.NoError(t, os.WriteFile(stub, []byte(selectionStub), 0700))
	state, observed := filepath.Join(dir, "state"), filepath.Join(dir, "observed")
	a := New(filepath.Join("..", "adapters", "sbx"))
	a.Env = []string{
		"SBX=" + stub, "SBX_TCK_STATE=" + state, "SBX_TCK_APP_NAME=selection-probe",
		"SBX_TCK_CAPABILITIES=" + claims, "STUB_OBSERVED=" + observed,
	}
	return a, observed
}

func TestSbxRejectCapabilitySkipsOrdinaryOptional(t *testing.T) {
	a, observed := selectionAdapter(t, "com.docker.sandbox/lifecycle@1")
	id, err := a.Create(t.Context(), []string{"workload", "ordinary-optional"}, CreateOptions{
		RejectCapabilities: []string{"com.docker.sandbox/lifecycle@1"},
	})
	require.NoError(t, err)
	raw, err := os.ReadFile(observed)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "ordinary-optional", "rejected optional kit must not reach sbx")
	require.NotContains(t, string(raw), "--reject-capability", "contract flag is adapter-side, not an sbx option")
	state, err := a.Selection(t.Context(), id)
	require.NoError(t, err)
	require.Empty(t, state.Selection.Selected)
	require.Len(t, state.Selection.Skipped, 1)
	require.Equal(t, []string{"capabilities[0]"}, state.Selection.Skipped[0].Rejected)
	require.Contains(t, state.Selection.Skipped[0].Source.Kit, "ordinary-optional")
	require.NoError(t, a.Remove(t.Context(), id))
}

func TestSbxGroupsPartialSkippedWhenMemberUnclaimed(t *testing.T) {
	for _, tc := range []struct {
		name, claims, rejected string
	}{
		{"volume-unclaimed", "com.docker.sandbox/lifecycle@1", "capabilities[0].group.capabilities[0]"},
		{"lifecycle-unclaimed", "com.docker.sandbox/volume@1", "capabilities[0].group.capabilities[1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, observed := selectionAdapter(t, tc.claims)
			id, err := a.Create(t.Context(), []string{"workload", "groups-partial"}, CreateOptions{})
			require.NoError(t, err)
			raw, err := os.ReadFile(observed)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "groups-partial", "unsatisfiable optional group must not reach sbx")
			state, err := a.Selection(t.Context(), id)
			require.NoError(t, err)
			surface, err := json.Marshal(state.Surface)
			require.NoError(t, err)
			require.Equal(t, "{}", string(surface))
			require.Len(t, state.Selection.Skipped, 1)
			rec := state.Selection.Skipped[0]
			require.Contains(t, rec.Source.Kit, "groups-partial")
			require.Equal(t, []string{tc.rejected}, rec.Rejected)
			require.Equal(t, []string{
				"capabilities[0].group.capabilities[0]",
				"capabilities[0].group.capabilities[1]",
			}, rec.Members)
			require.NoError(t, a.Remove(t.Context(), id))
		})
	}
}

func TestSbxGroupsRequiredRefusesWhenMemberUnclaimed(t *testing.T) {
	for _, tc := range []struct {
		name, claims, member string
	}{
		{"volume-unclaimed", "com.docker.sandbox/lifecycle@1", "capabilities[0].group.capabilities[0]"},
		{"lifecycle-unclaimed", "com.docker.sandbox/volume@1", "capabilities[0].group.capabilities[1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := selectionAdapter(t, tc.claims)
			_, err := a.Create(t.Context(), []string{"workload", "groups-required"}, CreateOptions{})
			var refused *RefusedError
			require.ErrorAs(t, err, &refused)
			require.Contains(t, refused.Detail, "groups-required")
			require.Contains(t, refused.Detail, tc.member)
		})
	}
}

func TestSbxSelectionPolicyAppliedOnRecreate(t *testing.T) {
	a, observed := selectionAdapter(t, "com.docker.sandbox/lifecycle@1")
	id, err := a.Create(t.Context(), []string{"workload", "ordinary-optional"}, CreateOptions{})
	require.NoError(t, err)
	before, err := a.Selection(t.Context(), id)
	require.NoError(t, err)
	require.Len(t, before.Selection.Selected, 1)
	require.Contains(t, before.Selection.Selected[0].Source.Kit, "ordinary-optional")
	require.NoError(t, a.RejectCapabilities(t.Context(), id, "com.docker.sandbox/lifecycle@1"))
	// Restart must keep the original decision.
	require.NoError(t, a.Stop(t.Context(), id))
	require.NoError(t, a.Start(t.Context(), id))
	afterStop, err := a.Selection(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, before, afterStop)
	require.NoError(t, a.Recreate(t.Context(), id))
	fresh, err := a.Selection(t.Context(), id)
	require.NoError(t, err)
	require.Empty(t, fresh.Selection.Selected)
	require.Len(t, fresh.Selection.Skipped, 1)
	require.Equal(t, []string{"capabilities[0]"}, fresh.Selection.Skipped[0].Rejected)
	lines := strings.Split(strings.TrimSpace(string(mustRead(t, observed))), "\n")
	require.GreaterOrEqual(t, len(lines), 2)
	require.Contains(t, lines[0], "ordinary-optional")
	require.NotContains(t, lines[len(lines)-1], "ordinary-optional")
	require.NoError(t, a.Remove(t.Context(), id))
}

func TestSbxGitIdentitySnapshotReplayedOnRecreate(t *testing.T) {
	a, observed := selectionAdapter(t, "com.docker.sandbox/git-identity@1")
	cfg := filepath.Join(t.TempDir(), "identity.gitconfig")
	require.NoError(t, os.WriteFile(cfg, []byte("[user]\n\tname = Alice\n\temail = alice@example.com\n"), 0600))
	id, err := a.Create(t.Context(), []string{"workload", "git-identity"}, CreateOptions{GitIdentityConfig: cfg})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg, []byte("[user]\n\tname = Eve\n\temail = eve@example.com\n"), 0600))
	require.NoError(t, a.Recreate(t.Context(), id))
	raw := string(mustRead(t, observed))
	require.NotContains(t, raw, cfg, "recreate must not reread the mutable suite path")
	require.Contains(t, raw, "git-identity-snapshot")
	// Argv carries the snapshot path; its contents must stay the create-time pair.
	var snapPath string
	for _, line := range strings.Split(raw, "\n") {
		for _, field := range strings.Fields(line) {
			if strings.Contains(field, "git-identity-snapshot") {
				snapPath = field
			}
		}
	}
	require.NotEmpty(t, snapPath)
	snapBody := string(mustRead(t, snapPath))
	require.Contains(t, snapBody, "Alice")
	require.NotContains(t, snapBody, "Eve")
	state, err := a.Selection(t.Context(), id)
	require.NoError(t, err)
	require.Len(t, state.Selection.Selected, 1)
	require.Contains(t, state.Selection.Selected[0].Source.Kit, "git-identity")
	require.NoError(t, a.Remove(t.Context(), id))
}

func TestSbxSSHAgentOptionalSelectionRecords(t *testing.T) {
	t.Run("skipped-without-agent", func(t *testing.T) {
		a, _ := selectionAdapter(t, "com.docker.sandbox/ssh-agent@1")
		id, err := a.Create(t.Context(), []string{"workload", "ssh-agent-optional"}, CreateOptions{})
		require.NoError(t, err)
		state, err := a.Selection(t.Context(), id)
		require.NoError(t, err)
		require.Empty(t, state.Selection.Selected)
		require.Len(t, state.Selection.Skipped, 1)
		require.Contains(t, state.Selection.Skipped[0].Source.Kit, "ssh-agent-optional")
		require.Equal(t, []string{"capabilities[0]"}, state.Selection.Skipped[0].Rejected)
		require.NoError(t, a.Remove(t.Context(), id))
	})
	t.Run("selected-with-agent", func(t *testing.T) {
		a, _ := selectionAdapter(t, "com.docker.sandbox/ssh-agent@1")
		socket := filepath.Join(t.TempDir(), "suite-agent.sock")
		id, err := a.Create(t.Context(), []string{"workload", "ssh-agent-optional"}, CreateOptions{SSHAgent: socket})
		require.NoError(t, err)
		state, err := a.Selection(t.Context(), id)
		require.NoError(t, err)
		require.Empty(t, state.Selection.Skipped)
		require.Len(t, state.Selection.Selected, 1)
		require.Contains(t, state.Selection.Selected[0].Source.Kit, "ssh-agent-optional")
		require.NoError(t, a.Remove(t.Context(), id))
	})
	t.Run("skipped-when-rejected", func(t *testing.T) {
		a, observed := selectionAdapter(t, "com.docker.sandbox/ssh-agent@1")
		socket := filepath.Join(t.TempDir(), "suite-agent.sock")
		id, err := a.Create(t.Context(), []string{"workload", "ssh-agent-optional"}, CreateOptions{
			SSHAgent:           socket,
			RejectCapabilities: []string{"com.docker.sandbox/ssh-agent@1"},
		})
		require.NoError(t, err)
		require.NotContains(t, string(mustRead(t, observed)), "ssh-agent-optional")
		state, err := a.Selection(t.Context(), id)
		require.NoError(t, err)
		require.Len(t, state.Selection.Skipped, 1)
		require.Equal(t, []string{"capabilities[0]"}, state.Selection.Skipped[0].Rejected)
		require.NoError(t, a.Remove(t.Context(), id))
	})
}

func TestSbxGitIdentityRestoredAfterClearedSelectionPolicy(t *testing.T) {
	a, observed := selectionAdapter(t, "com.docker.sandbox/git-identity@1")
	cfg := filepath.Join(t.TempDir(), "identity.gitconfig")
	require.NoError(t, os.WriteFile(cfg, []byte("[user]\n\tname = Alice\n\temail = alice@example.com\n"), 0600))
	id, err := a.Create(t.Context(), []string{"workload", "git-identity-optional"}, CreateOptions{
		GitIdentityConfig:  cfg,
		RejectCapabilities: []string{"com.docker.sandbox/git-identity@1"},
	})
	require.NoError(t, err)
	raw := string(mustRead(t, observed))
	require.NotContains(t, raw, "git-identity-snapshot", "rejected create must withhold identity from sbx")
	require.NotContains(t, raw, "git-identity-optional", "rejected optional identity kit must not reach sbx")
	state, err := a.Selection(t.Context(), id)
	require.NoError(t, err)
	require.Len(t, state.Selection.Skipped, 1)
	// Clear future rejections: recreate must restore the remembered snapshot.
	require.NoError(t, a.RejectCapabilities(t.Context(), id))
	require.NoError(t, a.Recreate(t.Context(), id))
	lines := strings.Split(strings.TrimSpace(string(mustRead(t, observed))), "\n")
	require.GreaterOrEqual(t, len(lines), 2)
	require.Contains(t, lines[len(lines)-1], "git-identity-snapshot")
	require.Contains(t, lines[len(lines)-1], "git-identity-optional")
	fresh, err := a.Selection(t.Context(), id)
	require.NoError(t, err)
	require.Len(t, fresh.Selection.Selected, 1)
	require.Contains(t, fresh.Selection.Selected[0].Source.Kit, "git-identity-optional")
	require.NoError(t, a.Remove(t.Context(), id))
}

func TestSbxGitIdentitySnapshotPreservesValues(t *testing.T) {
	a, observed := selectionAdapter(t, "com.docker.sandbox/git-identity@1")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "identity.gitconfig")
	marker := filepath.Join(dir, "executed")
	values := map[string]string{
		"user.name":  "Alice\n\t\"quoted\" \\path; $(touch " + marker + ")\n",
		"user.email": "alice@example.invalid",
	}
	for key, value := range values {
		require.NoError(t, exec.Command("git", "config", "--file", cfg, key, value).Run())
	}
	require.NoError(t, exec.Command("git", "config", "--file", cfg, "alias.source-only", "!false").Run())
	before := mustRead(t, cfg)
	id, err := a.Create(t.Context(), []string{"workload", "git-identity"}, CreateOptions{GitIdentityConfig: cfg})
	require.NoError(t, err)
	var snapshot string
	for _, arg := range strings.Fields(string(mustRead(t, observed))) {
		if strings.HasSuffix(arg, ".git-identity-snapshot") {
			snapshot = arg
		}
	}
	require.NotEmpty(t, snapshot)
	for key, want := range values {
		value, err := exec.Command("git", "config", "--file", snapshot, "--null", "--get", key).Output()
		require.NoError(t, err)
		require.Equal(t, want+"\x00", string(value), key)
	}
	keys, err := exec.Command("git", "config", "--file", snapshot, "--name-only", "--list").Output()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"user.name", "user.email"}, strings.Fields(string(keys)))
	require.NoFileExists(t, marker)
	require.Equal(t, before, mustRead(t, cfg), "snapshotting leaves the identity source intact")
	require.NoError(t, a.Remove(t.Context(), id))
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return raw
}

func TestSbxAdapterTransportsProcessInputsBeforeSandboxName(t *testing.T) {
	dir := t.TempDir()
	stub, observed := filepath.Join(dir, "sbx"), filepath.Join(dir, "argv")
	require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$OBSERVED\"\n"), 0700))
	a := New(filepath.Join("..", "adapters", "sbx"))
	a.Env = []string{"SBX=" + stub, "OBSERVED=" + observed, "SBX_TCK_APP_NAME="}
	_, err := a.ExecWith(t.Context(), "instance", ExecOptions{Cwd: "/tmp", Env: map[string]string{"SANDBOX_ID": "literal $HOME"}}, "probe", "--env", "argument")
	require.NoError(t, err)
	raw, err := os.ReadFile(observed)
	require.NoError(t, err)
	require.Equal(t, []string{"exec", "--workdir", "/tmp", "--env", "SANDBOX_ID=literal $HOME", "instance", "probe", "--env", "argument"}, strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00"))
}

func TestSbxAdapterReportsUnavailableWorkspaceScenarios(t *testing.T) {
	for _, mode := range []string{"host", "cloned", "none"} {
		a := New(filepath.Join("..", "adapters", "sbx"))
		a.Env = []string{"SBX_TCK_STATE=" + filepath.Join(t.TempDir(), "state")}
		_, err := a.Create(t.Context(), []string{"workload"}, CreateOptions{WorkspaceMode: mode})
		var refused *RefusedError
		require.ErrorAs(t, err, &refused)
		require.Equal(t, "unsupported workspace mode: "+mode, refused.Detail)
	}
}

func TestSbxExactContextSelectionMatrix(t *testing.T) {
	const low = "com.docker.sandbox/agent-context@1"
	const high = "com.docker.sandbox/agent-context@2"
	for supported := 0; supported < 4; supported++ {
		for rejected := 0; rejected < 4; rejected++ {
			for _, fixture := range []string{
				"exact-context-v1-optional", "exact-context-v1-required",
				"exact-context-v2-optional", "exact-context-v2-required",
				"exact-context-pair-optional", "exact-context-pair-required-low",
			} {
				t.Run(fmt.Sprintf("supported-%d/rejected-%d/%s", supported, rejected, fixture), func(t *testing.T) {
					var claims, rejects []string
					for i, typ := range []string{low, high} {
						if supported&(1<<i) != 0 {
							claims = append(claims, typ)
						}
						if rejected&(1<<i) != 0 {
							rejects = append(rejects, typ)
						}
					}
					a, observed := selectionAdapter(t, strings.Join(claims, ","))
					root, err := filepath.Abs(filepath.Join("..", "sandbox", "testdata", "fixtures"))
					require.NoError(t, err)
					kit := filepath.Join(root, fixture)
					original, err := spec.Decode(mustRead(t, filepath.Join(kit, fixture+".yaml")))
					require.NoError(t, err)
					var accepted []spec.Capability
					var selected, skipped []string
					var required []string
					for i, entry := range original.Capabilities {
						path := fmt.Sprintf("capabilities[%d]", i)
						if slices.Contains(claims, entry.Type) && !slices.Contains(rejects, entry.Type) {
							accepted = append(accepted, entry)
							selected = append(selected, path)
						} else {
							skipped = append(skipped, path)
							if !entry.Optional {
								required = append(required, path)
							}
						}
					}
					id, err := a.Create(t.Context(), []string{filepath.Join(root, "exact-context-workload"), kit}, CreateOptions{RejectCapabilities: rejects})
					if len(required) > 0 {
						var refused *RefusedError
						require.ErrorAs(t, err, &refused)
						require.Contains(t, refused.Detail, fixture)
						for _, path := range required {
							require.Contains(t, refused.Detail, path)
						}
						require.NoFileExists(t, observed, "required refusal must precede the sbx call")
						return
					}
					require.NoError(t, err)
					state, err := a.Selection(t.Context(), id)
					require.NoError(t, err)
					for _, records := range []struct {
						got     []spec.SelectionRecord
						paths   []string
						skipped bool
					}{{state.Selection.Selected, selected, false}, {state.Selection.Skipped, skipped, true}} {
						require.Len(t, records.got, len(records.paths))
						for i, rec := range records.got {
							path := records.paths[i]
							require.Equal(t, path, rec.Path)
							require.NotNil(t, rec.Source)
							require.Equal(t, fixture, rec.Source.Kit)
							require.Equal(t, path, rec.Source.Path)
							require.Equal(t, []string{path}, rec.Members)
							require.Len(t, rec.MemberSources, 1)
							require.Equal(t, *rec.Source, rec.MemberSources[0])
							if records.skipped {
								require.Equal(t, []string{path}, rec.Rejected)
							} else {
								require.Empty(t, rec.Rejected)
							}
						}
					}
					var consumed string
					argv := strings.Fields(string(mustRead(t, observed)))
					for i, arg := range argv {
						if arg == "--kit" {
							consumed = argv[i+1]
						}
					}
					if len(accepted) == 0 {
						require.Empty(t, consumed, "skipped exact requests must not reach sbx")
					} else {
						require.NotEmpty(t, consumed)
						require.Equal(t, fixture, filepath.Base(consumed), "@1 staging retains the authored kit name")
						effective, err := spec.Decode(mustRead(t, filepath.Join(consumed, fixture+".yaml")))
						require.NoError(t, err)
						require.Equal(t, accepted, effective.Capabilities, "sbx must receive each selected request with its authored config")
						if len(accepted) < len(original.Capabilities) {
							require.Equal(t, mustRead(t, filepath.Join(kit, "context.txt")), mustRead(t, filepath.Join(consumed, "context.txt")))
						}
					}
					require.NoError(t, a.Remove(t.Context(), id))
					if consumed != "" && consumed != kit {
						require.NoDirExists(t, filepath.Dir(consumed), "adapter-owned filtered fixtures must be removed")
					}
				})
			}
		}
	}
}

func TestSbxExactContextRequiredRefusalNamesEveryUnavailableRequest(t *testing.T) {
	a, observed := selectionAdapter(t, "")
	_, err := a.Create(t.Context(), []string{"exact-context-workload", "exact-context-v1-required", "exact-context-v2-required"}, CreateOptions{})
	var refused *RefusedError
	require.ErrorAs(t, err, &refused)
	for _, version := range []string{"1", "2"} {
		require.Contains(t, refused.Detail, "exact-context-v"+version+"-required capabilities[0]")
		require.Contains(t, refused.Detail, "com.docker.sandbox/agent-context@"+version)
	}
	require.NoFileExists(t, observed)
}

func TestSbxExactContextPairRestoresRequestsOnRecreate(t *testing.T) {
	const low = "com.docker.sandbox/agent-context@1"
	const high = "com.docker.sandbox/agent-context@2"
	a, observed := selectionAdapter(t, low+","+high)
	stateDir := filepath.Join(t.TempDir(), "state with spaces")
	stub := filepath.Join(t.TempDir(), "sbx")
	script := strings.Replace(selectionStub, "  shift\n  printf", "  shift\n  previous=\n  for arg do\n    if [ \"$previous\" = --kit ]; then [ -d \"$arg\" ]; fi\n    previous=\"$arg\"\n  done\n  printf", 1)
	require.NoError(t, os.WriteFile(stub, []byte(script), 0700))
	a.Env = append(a.Env, "SBX="+stub, "SBX_TCK_STATE="+stateDir)
	root, err := filepath.Abs(filepath.Join("..", "sandbox", "testdata", "fixtures"))
	require.NoError(t, err)
	fixture := "exact-context-pair-optional"
	kit := filepath.Join(root, fixture)
	id, err := a.Create(t.Context(), []string{filepath.Join(root, "exact-context-workload"), kit}, CreateOptions{RejectCapabilities: []string{high}})
	require.NoError(t, err)
	filtered := filepath.Join(stateDir, id+".exact-context", fixture, fixture+".yaml")
	for _, rejected := range []string{high, low, ""} {
		if rejected == "" {
			require.NoError(t, a.RejectCapabilities(t.Context(), id))
		} else {
			require.NoError(t, a.RejectCapabilities(t.Context(), id, rejected))
		}
		require.NoError(t, a.Recreate(t.Context(), id))
		state, err := a.Selection(t.Context(), id)
		require.NoError(t, err)
		if rejected == "" {
			require.Len(t, state.Selection.Selected, 2)
			require.Empty(t, state.Selection.Skipped)
			lines := strings.Split(strings.TrimSpace(string(mustRead(t, observed))), "\n")
			require.Contains(t, lines[len(lines)-1], "--kit "+kit)
		} else {
			require.Len(t, state.Selection.Selected, 1)
			require.Len(t, state.Selection.Skipped, 1)
			desc, err := spec.Decode(mustRead(t, filtered))
			require.NoError(t, err)
			require.Len(t, desc.Capabilities, 1)
			want := low
			if rejected == low {
				want = high
			}
			require.Equal(t, want, desc.Capabilities[0].Type)
		}
	}
	require.NoError(t, a.Remove(t.Context(), id))
	require.NoDirExists(t, filepath.Dir(filepath.Dir(filtered)))
}

func TestSbxExactContextFilteringFailureDoesNotReachRuntime(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		t.Run(fmt.Sprintf("recreate-%t", recreate), func(t *testing.T) {
			a, observed := selectionAdapter(t, "com.docker.sandbox/agent-context@1")
			stateDir := filepath.Join(t.TempDir(), "state")
			removed := filepath.Join(t.TempDir(), "removed")
			stub := filepath.Join(t.TempDir(), "sbx")
			script := strings.Replace(selectionStub, "rm | stop) ;;", "rm) touch \"$STUB_REMOVED\" ;;\nstop) ;;", 1)
			require.NoError(t, os.WriteFile(stub, []byte(script), 0700))
			a.Env = append(a.Env, "SBX="+stub, "STUB_REMOVED="+removed, "SBX_TCK_STATE="+stateDir)
			fixture := "exact-context-pair-optional"
			kit := filepath.Join(t.TempDir(), "fixtures with spaces", fixture)
			require.NoError(t, os.CopyFS(kit, os.DirFS(filepath.Join("..", "sandbox", "testdata", "fixtures", fixture))))
			var id string
			if recreate {
				var err error
				id, err = a.Create(t.Context(), []string{"exact-context-workload", kit}, CreateOptions{})
				require.NoError(t, err)
			}
			require.NoError(t, os.Remove(filepath.Join(kit, "context.txt")))
			if recreate {
				before, err := a.Selection(t.Context(), id)
				require.NoError(t, err)
				err = a.Recreate(t.Context(), id)
				require.ErrorContains(t, err, "could not filter exact-context fixtures")
				require.NoFileExists(t, removed, "failed preparation must not remove the existing instance")
				after, err := a.Selection(t.Context(), id)
				require.NoError(t, err)
				require.Equal(t, before, after)
				lines := strings.Split(strings.TrimSpace(string(mustRead(t, observed))), "\n")
				require.Len(t, lines, 1, "failed preparation must not attempt a replacement create")
				require.NoError(t, a.Remove(t.Context(), id))
			} else {
				_, err := a.Create(t.Context(), []string{"exact-context-workload", kit}, CreateOptions{})
				require.ErrorContains(t, err, "could not filter exact-context fixtures")
				require.NoFileExists(t, observed)
				entries, err := os.ReadDir(stateDir)
				require.NoError(t, err)
				require.Empty(t, entries, "failed preparation must release adapter state")
			}
		})
	}
}

func TestSbxExactContextMixedVersionConflictComesFromRuntime(t *testing.T) {
	for _, fixtures := range [][]string{
		{"exact-context-v1-optional", "exact-context-v2-optional"},
		{"exact-context-pair-optional"},
	} {
		t.Run(strings.Join(fixtures, "+"), func(t *testing.T) {
			a, observed := selectionAdapter(t, "com.docker.sandbox/agent-context@1,com.docker.sandbox/agent-context@2")
			stub := filepath.Join(t.TempDir(), "sbx")
			detail := "request failed: 400: mixed agent-context contracts"
			script := strings.Replace(selectionStub, "  printf '%s\\n' \"$*\" >>\"$STUB_OBSERVED\"", "  printf '%s\\n' \"$*\" >>\"$STUB_OBSERVED\"\n  echo '"+detail+"' >&2\n  exit 3", 1)
			require.NoError(t, os.WriteFile(stub, []byte(script), 0700))
			a.Env = append(a.Env, "SBX="+stub)
			root, err := filepath.Abs(filepath.Join("..", "sandbox", "testdata", "fixtures"))
			require.NoError(t, err)
			kits := []string{filepath.Join(root, "exact-context-workload")}
			for _, fixture := range fixtures {
				kits = append(kits, filepath.Join(root, fixture))
			}
			_, err = a.Create(t.Context(), kits, CreateOptions{})
			var refused *RefusedError
			require.ErrorAs(t, err, &refused)
			require.Equal(t, detail, refused.Detail)
			argv := string(mustRead(t, observed))
			for _, kit := range kits[1:] {
				require.Contains(t, argv, "--kit "+kit, "the runtime receives every eligible exact request")
			}
		})
	}
}
