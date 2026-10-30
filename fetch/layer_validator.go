package fetch

import (
	"context"
	"fmt"

	"github.com/docker/sandbox-kit-spec/v3/resolve"
)

// LayerValidator checks the whole Kit set, including cross-Kit effects.
// Assemble calls it once after validating metadata and resolving declarations.
// kits contains selected, dependency-ordered units; loaded maps their original
// references to verified metadata and optional layer readers. Treat both as
// read-only. A validator returns any failure and honors ctx. It may report
// progress synchronously through report when non-nil.
type LayerValidator func(ctx context.Context, kits []*resolve.Unit, loaded map[string]*LoadedKit, report func(Progress)) error

// InventoryLimits bounds cumulative filesystem inventory work across the entire
// composition, including repeated/cached layers. Zero fields use the defaults;
// negative fields are rejected. Increasing limits increases memory/CPU budgets.
// These are consumer resource policy, never values supplied by a Kit descriptor.
type InventoryLimits struct {
	Layers     int
	Entries    int
	PathBytes  int
	Components int
}

func (l InventoryLimits) normalized() (InventoryLimits, error) {
	for _, pair := range []struct {
		value    *int
		fallback int
	}{
		{&l.Layers, maxInventoryLayers}, {&l.Entries, maxInventoryEntries},
		{&l.PathBytes, maxInventoryPathBytes}, {&l.Components, maxInventoryComponents},
	} {
		if *pair.value < 0 {
			return InventoryLimits{}, fmt.Errorf("inventory limits must be nonnegative")
		}
		if *pair.value == 0 {
			*pair.value = pair.fallback
		}
	}
	return l, nil
}

// LayerValidatorWithLimits retains all default integrity, extraction and collision
// checks with explicit inventory budgets for consumers handling larger images.
// Each invocation gets a fresh operation-wide budget; the validator is reusable.
func LayerValidatorWithLimits(limits InventoryLimits) LayerValidator {
	return func(ctx context.Context, kits []*resolve.Unit, loaded map[string]*LoadedKit, report func(Progress)) error {
		normalized, err := limits.normalized()
		if err != nil {
			return err
		}
		inventories, err := inventoryKits(ctx, kits, loaded, report, normalized)
		if err != nil {
			return err
		}
		return progressStep(ctx, report, Progress{Stage: StageCollisions}, func() error { return checkFileCollisions(ctx, inventories) })
	}
}

// DefaultLayerValidator is the built-in LayerValidator. It verifies blob digests and
// diff IDs, enforces inventory limits, models safe extraction, and checks
// cross-Kit file collisions in workload-first image order. Every Kit needs a
// LayerLoader. It reports inventory and collision stages and closes all streams.
// Inputs must contain the validated metadata supplied by Assemble.
func DefaultLayerValidator(ctx context.Context, kits []*resolve.Unit, loaded map[string]*LoadedKit, report func(Progress)) error {
	return LayerValidatorWithLimits(InventoryLimits{})(ctx, kits, loaded, report)
}
