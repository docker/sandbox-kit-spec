package resolve

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

func TestNativeChatGPTOwnsOpenAIRuntimeCredential(t *testing.T) {
	for _, phase := range []string{"install", "runtime"} {
		for _, reverse := range []bool{false, true} {
			native := unit("reg.io/native:1", spec.KindWorkload)
			native.Descriptor.Capabilities = []spec.Capability{{Type: spec.CapabilityOpenAIChatGPT, Config: map[string]any{"path": "~/.codex/auth.json"}}}
			generic := unit("reg.io/generic:1", spec.KindMixin)
			generic.Descriptor.Capabilities = []spec.Capability{{Type: spec.CapabilityCredential, Config: map[string]any{
				"service": "openai", "phase": phase, "apiKey": map[string]any{"name": "OPENAI_API_KEY"},
			}}}
			units := []*Unit{native, generic}
			if reverse {
				units[0], units[1] = units[1], units[0]
			}
			_, err := Resolve(units)
			if phase == "runtime" {
				require.ErrorContains(t, err, "credential (openai, runtime) is declared by more than one kit")
			} else {
				require.NoError(t, err)
			}
		}
	}
}
