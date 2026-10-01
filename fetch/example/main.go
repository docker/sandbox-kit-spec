// Command example assembles published Kits and checks their layer file
// collisions through the runtime convenience API. It reads
// []fetch.Request as JSON from stdin and writes typed results to stdout.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/fetch"
	"github.com/docker/sandbox-kit-spec/v3/spec"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func main() {
	supportedTypes := flag.String("supported-types", strings.Join(spec.KnownCapabilities(), ","), "comma-separated capability types to accept in this preview")
	allowVolumes := flag.Bool("allow-volumes", true, "simulate host policy allowing persistent volumes")
	flag.Parse()

	claimed := strings.Split(*supportedTypes, ",")
	for i := range claimed {
		claimed[i] = strings.TrimSpace(claimed[i])
	}
	supported := spec.Supported(claimed...)
	selectCapability := func(ctx context.Context, kit spec.Descriptor, capability spec.Capability) spec.CapabilityDecision {
		if decision := supported(ctx, kit, capability); !decision.Accepted {
			return decision
		}
		// A real runtime uses host availability and policy here. The full
		// entry includes expanded config and kit.DisplayName labels the owning
		// Kit; deciding must not apply effects.
		if capability.Type == spec.CapabilityVolume && !*allowVolumes {
			return spec.CapabilityDecision{Message: "persistent volumes disabled by host policy"}
		}
		return spec.CapabilityDecision{Accepted: true}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := run(ctx, selectCapability); err != nil {
		// Validation errors already include source excerpts and locations.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, selectCapability spec.SelectCapability) error {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read requests: %w", err)
	}
	var requests []fetch.Request
	if err := json.Unmarshal(raw, &requests); err != nil {
		return fmt.Errorf("decode requests: %w", err)
	}
	// The API owns loading, atomic selection, composition, and layer checks.
	result, err := fetch.Assemble(ctx, requests, fetch.Options{
		LayerValidator:     fetch.DefaultLayerValidator,
		CapabilitySelector: selectCapability,
	})
	if err != nil {
		return err
	}
	manifest, err := result.Image.Manifest()
	if err != nil {
		return err
	}
	// The runtime imports the image and applies Environment and WorkingDir.
	// Persist Resolved.Descriptor and Resolved.Selections for restart; only a
	// fresh creation resolves and selects again.
	output := struct {
		*fetch.Result
		Manifest ocispec.Manifest
	}{result, manifest}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}
