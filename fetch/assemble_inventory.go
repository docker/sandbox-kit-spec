package fetch

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
	"github.com/docker/sandbox-kit-spec/v3/resolve"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type kitInventory struct {
	reference string
	layers    [][]inventoryEntry
}

// Retain only fields used by the extraction model. tar.Header also carries
// timestamps, format information and extended-attribute maps that multiply
// memory usage even when zeroed.
type inventoryEntry struct {
	name, linkname     string
	mode               int64
	uid, gid           int
	devmajor, devminor int64
	typeflag           byte
}

func (e inventoryEntry) header() tar.Header {
	return tar.Header{Name: e.name, Linkname: e.linkname, Typeflag: e.typeflag,
		Mode: e.mode, Uid: e.uid, Gid: e.gid, Devmajor: e.devmajor, Devminor: e.devminor}
}

// These are operation-wide limits, including cache replays: compact cached
// entries still produce extraction trees each time they are applied. Count
// layers as well so empty archives cannot bypass the entry budget. Component
// counts bound implied directory creation independently of name byte lengths.
const (
	maxInventoryLayers     = 4096
	maxInventoryEntries    = 250_000
	maxInventoryPathBytes  = 32 << 20
	maxInventoryComponents = 250_000
)

type inventoryBudget struct {
	limits         InventoryLimits
	layersLeft     int
	entriesLeft    int
	pathBytesLeft  int
	componentsLeft int
}

func (b *inventoryBudget) limit(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

func (b *inventoryBudget) consumeLayer() error {
	if b.layersLeft == 0 {
		return fmt.Errorf("assembly inventory exceeds %d layers", b.limit(b.limits.Layers, maxInventoryLayers))
	}
	b.layersLeft--
	return nil
}

func (b *inventoryBudget) consume(hdr *tar.Header) error {
	if b.entriesLeft == 0 {
		return fmt.Errorf("assembly inventory exceeds %d entries", b.limit(b.limits.Entries, maxInventoryEntries))
	}
	if len(hdr.Name) > b.pathBytesLeft || len(hdr.Linkname) > b.pathBytesLeft-len(hdr.Name) {
		return fmt.Errorf("assembly inventory exceeds %d path and link-name bytes", b.limit(b.limits.PathBytes, maxInventoryPathBytes))
	}
	// Charge uncleaned paths and link targets conservatively: normalization
	// must not hide the work of building implied directories or resolving links.
	components := pathComponents(hdr.Name) + pathComponents(hdr.Linkname)
	if components > b.componentsLeft {
		return fmt.Errorf("assembly inventory exceeds %d path components", b.limit(b.limits.Components, maxInventoryComponents))
	}
	b.componentsLeft -= components
	b.entriesLeft--
	b.pathBytesLeft -= len(hdr.Name) + len(hdr.Linkname)
	return nil
}

// Count without allocating a slice proportional to an untrusted path's depth.
func pathComponents(name string) int {
	count, inComponent := 0, false
	for i := 0; i < len(name); i++ {
		if name[i] == '/' {
			inComponent = false
		} else if !inComponent {
			count++
			inComponent = true
		}
	}
	return count
}

func inventoryKits(ctx context.Context, kits []*resolve.Unit, loaded map[string]*LoadedKit, report func(Progress), limits InventoryLimits) ([]kitInventory, error) {
	// Descriptors are dependency-ordered; filesystem effects are workload-first.
	resolution, err := resolve.Resolve(kits)
	if err != nil {
		return nil, err
	}
	type blobKey struct {
		digest digest.Digest
		size   int64
		diffID digest.Digest
	}
	cache := map[blobKey][]inventoryEntry{}
	budget := inventoryBudget{limits: limits, layersLeft: limits.Layers, entriesLeft: limits.Entries, pathBytesLeft: limits.PathBytes, componentsLeft: limits.Components}
	inventories := make([]kitInventory, 0, len(kits))
	for _, kit := range resolution.Ordered() {
		input := loaded[kit.Reference]
		if input == nil {
			return nil, fmt.Errorf("inventory %s: missing loaded kit", kit.Reference)
		}
		if input.LayerLoader == nil {
			return nil, fmt.Errorf("inventory %s: missing LayerLoader", kit.Reference)
		}
		if len(input.Config.RootFS.DiffIDs) != len(input.Manifest.Layers) {
			return nil, fmt.Errorf("inventory %s: image config rootfs does not match manifest layers", kit.Reference)
		}
		inventory := kitInventory{reference: kit.Reference}
		for index, layer := range input.Manifest.Layers {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			diffID := input.Config.RootFS.DiffIDs[index]
			key := blobKey{layer.Digest, layer.Size, diffID}
			files, exists := cache[key]
			err := progressStep(ctx, report, Progress{Stage: StageInventory, Reference: kit.Reference, Layer: layer.Digest}, func() error {
				if err := budget.consumeLayer(); err != nil {
					return err
				}
				if exists {
					for _, entry := range files {
						if err := ctx.Err(); err != nil {
							return err
						}
						hdr := entry.header()
						if err := budget.consume(&hdr); err != nil {
							return err
						}
					}
					return nil
				}
				var err error
				files, err = readLayerInventory(ctx, input, layer, diffID, &budget)
				return err
			})
			if err != nil {
				return nil, fmt.Errorf("inventory %s layer %s: %w", kit.Reference, layer.Digest, err)
			}
			cache[key] = files
			inventory.layers = append(inventory.layers, files)
		}
		inventories = append(inventories, inventory)
	}
	return inventories, nil
}

func readLayerInventory(ctx context.Context, input *LoadedKit, layer ocispec.Descriptor, diffID digest.Digest, budget *inventoryBudget) (files []inventoryEntry, retErr error) {
	if layer.Size == math.MaxInt64 {
		return nil, fmt.Errorf("layer size is too large")
	}
	reader, err := input.LayerLoader(ctx, layer)
	if err != nil {
		return nil, err
	}
	if reader == nil {
		return nil, fmt.Errorf("LayerLoader returned no stream")
	}
	defer func() { retErr = errors.Join(retErr, reader.Close()) }()
	// The tar reader can finish before the compressed blob does. Drain through
	// the same verifier so trailing bytes, short blobs, and truncation are judged.
	limited := &io.LimitedReader{R: contextReader{ctx: ctx, reader: reader}, N: layer.Size + 1}
	verifier := layer.Digest.Verifier()
	verified := io.TeeReader(limited, verifier)
	err = assemble.WalkLayerVerified(verified, diffID, func(hdr *tar.Header) error {
		if err := budget.consume(hdr); err != nil {
			return err
		}
		// Clone names so a short substring cannot retain a larger PAX record.
		// Keep only extraction metadata, never bodies or extended attributes.
		files = append(files, inventoryEntry{name: strings.Clone(hdr.Name), typeflag: hdr.Typeflag,
			linkname: strings.Clone(hdr.Linkname), mode: hdr.Mode, uid: hdr.Uid, gid: hdr.Gid,
			devmajor: hdr.Devmajor, devminor: hdr.Devminor})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(io.Discard, verified); err != nil {
		return nil, err
	}
	if layer.Size+1-limited.N != layer.Size {
		return nil, fmt.Errorf("layer size mismatch")
	}
	if !verifier.Verified() {
		return nil, fmt.Errorf("layer digest mismatch")
	}
	return files, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
