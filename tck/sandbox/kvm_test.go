package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
	"github.com/stretchr/testify/require"
)

func TestKVMProbeRejectsUnusableDevices(t *testing.T) {
	ordinary := filepath.Join(t.TempDir(), "ordinary")
	require.NoError(t, os.WriteFile(ordinary, []byte("not a device"), 0600))
	for _, mode := range []string{"kvm", "vsock"} {
		for _, path := range []string{filepath.Join(t.TempDir(), "missing"), ordinary, "/dev/null"} {
			t.Run(mode+"/"+filepath.Base(path), func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), "python3", filepath.Join("testdata", "fixtures", "workload", "kit-tck-kvm.py"), mode, "--"+mode, path)
				output, err := cmd.CombinedOutput()
				require.Error(t, err, "a missing or merely readable path cannot establish usable virtualization")
				var status *exec.ExitError
				require.ErrorAs(t, err, &status)
				require.Equal(t, 3, status.ExitCode())
				require.Contains(t, string(output), "unavailable:"+mode+":"+path+":")
				require.NotContains(t, string(output), "usable\n")
			})
		}
	}
}

func TestKVMChecksPass(t *testing.T) {
	var selected []check
	for _, c := range checks {
		if c.capability == capKVM {
			selected = append(selected, c)
		}
	}
	require.Len(t, selected, 7)
	for _, broken := range []string{"", "kvm-refuses-transition"} {
		t.Run(broken, func(t *testing.T) {
			rep := runAgainstFake(t, broken, selected...)
			require.False(t, rep.Failed(), "%s", rep)
		})
	}
}

// Isolating KVM ensures another unsupported request cannot hide a missing
// entry in the generic required-unclaimed refusal matrix.
func TestKVMRequiredUnclaimedIsRefused(t *testing.T) {
	for _, broken := range []string{"", "accepts-required-unclaimed"} {
		t.Run(broken, func(t *testing.T) {
			claimed := make(map[string]bool)
			var claims []string
			for _, typ := range spec.KnownCapabilities() {
				if typ != capKVM {
					claimed[typ] = true
					claims = append(claims, typ)
				}
			}
			a := adapter.New(filepath.Join("testdata", "fake-adapter"))
			a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + strings.Join(claims, ","), "KIT_TCK_FAKE_BROKEN=" + broken}
			e := &Env{Adapter: a, Fixtures: Fixtures(FixtureDir), Claimed: claimed}
			for _, c := range checks {
				if c.requirement != "conformance.md §2.2/required-unclaimed-refused" {
					continue
				}
				findings := c.run(t.Context(), e)
				if broken == "" {
					require.Empty(t, findings)
				} else {
					require.Len(t, findings, 1)
					require.Equal(t, report.Fail, findings[0].Severity)
					require.Contains(t, findings[0].Detail, capKVM)
				}
				return
			}
			t.Fatal("required-unclaimed refusal check is missing")
		})
	}
}

// KVM startup is observable through the workload even when the runtime
// implements no hook capability. Requiring lifecycle would hide late access.
func TestKVMBeforeStartupWithoutLifecycle(t *testing.T) {
	for _, broken := range []string{"", "kvm-late-access", "kvm-restart-late-access"} {
		t.Run(broken, func(t *testing.T) {
			a := adapter.New(filepath.Join("testdata", "fake-adapter"))
			a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + capKVM, "KIT_TCK_FAKE_BROKEN=" + broken}
			for _, c := range checks {
				if c.requirement != "kvm@1/before-startup" {
					continue
				}
				rep, err := runChecks(t.Context(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}, []check{c})
				require.NoError(t, err)
				if broken == "" {
					require.Empty(t, rep.Findings, "KVM-only startup must be judged rather than skipped")
				} else {
					require.Contains(t, failedRequirements(rep), c.requirement, "late access must fail without lifecycle support")
				}
				return
			}
			t.Fatal("KVM startup check is missing")
		})
	}
}

// An optional contributor cannot weaken a required request supplied by
// another Kit, regardless of composition order.
func TestKVMFakeRequiredContributorWins(t *testing.T) {
	for _, required := range []string{"kvm", "kvm-second", "kvm-hooks"} {
		for _, refs := range [][]string{{required, "kvm-optional"}, {"kvm-optional", required}} {
			t.Run(strings.Join(refs, "+"), func(t *testing.T) {
				a := adapter.New(filepath.Join("testdata", "fake-adapter"))
				a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + capKVM, "KIT_TCK_FAKE_BROKEN="}
				_, err := a.Create(t.Context(), refs, adapter.CreateOptions{RejectCapabilities: []string{capKVM}})
				var refused *adapter.RefusedError
				require.ErrorAs(t, err, &refused)
				require.Contains(t, refused.Detail, capKVM)
			})
		}
	}
}

// Recreation must use future policy while a refusal keeps the current
// composition and grants. Exercise the protocol rather than the state file.
func TestKVMFakeRecreateSelection(t *testing.T) {
	for _, unavailable := range []string{"unclaimed", "create-rejection", "changed-policy"} {
		for _, refs := range [][]string{
			{"kvm"}, {"kvm-second"}, {"kvm-hooks"},
			{"kvm", "kvm-optional"}, {"kvm-optional", "kvm"},
			{"kvm-second", "kvm-optional"}, {"kvm-optional", "kvm-second"},
			{"kvm-hooks", "kvm-optional"}, {"kvm-optional", "kvm-hooks"},
			{"kvm-optional"}, nil,
		} {
			t.Run(unavailable+"/"+strings.Join(refs, "+"), func(t *testing.T) {
				claims := capKVM
				if unavailable == "unclaimed" {
					claims = groupVolume
					// The fake's empty claim override means its default claims.
				}
				a := adapter.New(filepath.Join("testdata", "fake-adapter"))
				a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + claims, "KIT_TCK_FAKE_BROKEN="}
				opts := adapter.CreateOptions{}
				if unavailable == "create-rejection" {
					opts.RejectCapabilities = []string{capKVM}
				}
				id, err := a.Create(t.Context(), []string{"kvm-optional"}, opts)
				require.NoError(t, err)
				if unavailable == "changed-policy" {
					require.NoError(t, a.RejectCapabilities(t.Context(), id, capKVM))
				}
				before, err := a.Selection(t.Context(), id)
				require.NoError(t, err)
				wasAccepted := unavailable == "changed-policy"
				assertAccess := func(accepted bool) {
					t.Helper()
					for _, mode := range []string{"kvm", "vsock"} {
						result, execErr := a.Exec(t.Context(), id, "kit-tck-kvm", mode)
						require.NoError(t, execErr)
						if accepted {
							require.Zero(t, result.ExitCode, "%s: %s", mode, result.Stderr)
						} else {
							require.Equal(t, 3, result.ExitCode, "%s must remain unavailable", mode)
						}
					}
				}
				assertAccess(wasAccepted)
				err = a.RecreateWith(t.Context(), id, refs, nil)
				optional := len(refs) == 0 || len(refs) == 1 && refs[0] == "kvm-optional"
				if optional {
					require.NoError(t, err)
				} else {
					var refused *adapter.RefusedError
					require.ErrorAs(t, err, &refused)
					require.Contains(t, refused.Detail, capKVM)
				}
				after, err := a.Selection(t.Context(), id)
				require.NoError(t, err)
				if optional {
					require.Empty(t, after.Selection.Selected)
					require.Len(t, after.Selection.Skipped, 1)
					require.Equal(t, "kvm-optional", after.Selection.Skipped[0].Source.Kit)
					require.Equal(t, []string{"capabilities[0]"}, after.Selection.Skipped[0].Rejected)
					require.Empty(t, after.Surface.Services)
					assertAccess(false)
				} else {
					require.Equal(t, before, after, "refusal must preserve the effective composition")
					assertAccess(wasAccepted)
				}
				if unavailable != "unclaimed" {
					require.NoError(t, a.RejectCapabilities(t.Context(), id))
					require.NoError(t, a.Recreate(t.Context(), id))
					restored, err := a.Selection(t.Context(), id)
					require.NoError(t, err)
					require.Len(t, restored.Selection.Selected, 1)
					require.Equal(t, "kvm-optional", restored.Selection.Selected[0].Source.Kit)
					require.Empty(t, restored.Selection.Skipped)
					require.Equal(t, []string{capKVM}, restored.Surface.Services)
					assertAccess(true)
					result, err := a.Exec(t.Context(), id, "cat", "/var/tmp/kit-tck-workload-kvm")
					require.NoError(t, err)
					require.Zero(t, result.ExitCode, "recreation must grant access before startup")
				}
			})
		}
	}
}
