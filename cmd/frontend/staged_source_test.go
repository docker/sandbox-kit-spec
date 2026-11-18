package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/moby/buildkit/client/llb"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	fstypes "github.com/tonistiigi/fsutil/types"
)

func TestReadKitDescriptorSelectsPublishedSource(t *testing.T) {
	const source = "schemaVersion: \"3\"\nkind: mixin\nversion: \"1.0.0\"\nprovides: [chosen]\n"
	const member = "schemaVersion: \"3\"\nkind: mixin\nversion: \"1.0.0\"\nprovides: [member]\n"
	for _, tc := range []struct {
		name   string
		labels map[string]string
		files  map[string]string
		want   string
		err    string
	}{
		{"labeled set", map[string]string{spec.LabelSourceStem: "set"}, map[string]string{"set": source, "member": member}, "set", ""},
		{"legacy single", nil, map[string]string{"original": source}, "original", ""},
		{"legacy ambiguous", nil, map[string]string{"set": source, "member": member}, "", "carries staged sources for 2 kits"},
		{"missing target", map[string]string{spec.LabelSourceStem: "missing"}, map[string]string{"original": source}, "", "names missing staged descriptor"},
		{"malformed target", map[string]string{spec.LabelSourceStem: "bad"}, map[string]string{"bad": "broken: [", "original": source}, "", "does not decode"},
		{"invalid target", map[string]string{spec.LabelSourceStem: "bad"}, map[string]string{"bad": "schemaVersion: \"3\"\nkind: mixin\nprovides: [unversioned]\n", "original": source}, "", "not a valid published descriptor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &stagedSourceClient{files: tc.files}
			d, stems, err := readKitDescriptor(t.Context(), c, llb.Scratch(), ocispecs.Platform{}, tc.labels)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				require.Nil(t, d)
				require.Empty(t, stems)
				return
			}
			require.NoError(t, err)
			require.Contains(t, stems, tc.want)
			require.Len(t, stems, len(tc.files))
			require.Equal(t, []string{"chosen"}, d.Provides)
		})
	}
	for _, stem := range []string{"", ".", "..", "../member", "/member", "member/child", "member\\child", "bad\x00stem", "line\nbreak", "${{ kit.args.name }}", "${{ kit.env.KIT }}"} {
		t.Run(fmt.Sprintf("invalid label %q", stem), func(t *testing.T) {
			c := &stagedSourceClient{files: map[string]string{"original": source}}
			d, _, err := readKitDescriptor(t.Context(), c, llb.Scratch(), ocispecs.Platform{}, map[string]string{spec.LabelSourceStem: stem})
			require.ErrorContains(t, err, "invalid staged source stem")
			require.Nil(t, d)
			require.Empty(t, c.reads, "an invalid label cannot read outside its staged source directory")
		})
	}
}

func TestPublishedSourceStemReplacesInheritedIdentity(t *testing.T) {
	for _, inherited := range []map[string]string{nil, {spec.LabelSourceStem: "base", "example.label": "preserved"}} {
		before := ocispecs.Image{Config: ocispecs.ImageConfig{Labels: inherited, Env: []string{"VALUE=unchanged"}, Entrypoint: []string{"/bin/agent"}}}
		raw, err := json.Marshal(before)
		require.NoError(t, err)
		published, err := withPublishedSourceStem(raw, "current")
		require.NoError(t, err)
		var after ocispecs.Image
		require.NoError(t, json.Unmarshal(published, &after))
		require.Equal(t, "current", after.Config.Labels[spec.LabelSourceStem])
		require.Equal(t, before.Config.Env, after.Config.Env)
		require.Equal(t, before.Config.Entrypoint, after.Config.Entrypoint)
		if inherited != nil {
			require.Equal(t, "preserved", after.Config.Labels["example.label"])
			require.Equal(t, "base", inherited[spec.LabelSourceStem])
		}
	}
}

func TestPublishedSourceStemPreservesImageExtensions(t *testing.T) {
	input := []byte(`{"architecture":"amd64","os":"linux","extension":9007199254740993,"config":{"Healthcheck":{"Test":["CMD","true"]},"Labels":{"example":"kept"}}}`)
	published, err := withPublishedSourceStem(input, "current")
	require.NoError(t, err)
	require.JSONEq(t, `{"architecture":"amd64","os":"linux","extension":9007199254740993,"config":{"Healthcheck":{"Test":["CMD","true"]},"Labels":{"example":"kept","vnd.docker.sandbox.kit.source-stem":"current"}}}`, string(published))
	require.Contains(t, string(published), "9007199254740993")
}

func TestNestedSourceRootsCannotBeOverwritten(t *testing.T) {
	nested := resolvedKit{reference: "nested", sourceStems: []string{"member", "nested"}}
	require.NoError(t, checkStemCollision("outer", []resolvedKit{nested}))
	require.ErrorContains(t, checkStemCollision("member", []resolvedKit{nested}), "where this set would stage its own")
	other := resolvedKit{reference: "other", sourceStems: []string{"other", "member"}}
	require.ErrorContains(t, checkKitCollisions([]resolvedKit{nested, other}), "both stage their sources at")
	other.sourceStems = []string{"other", "distinct"}
	require.NoError(t, checkKitCollisions([]resolvedKit{nested, other}))
}

type stagedSourceClient struct {
	fakeGatewayClient
	files map[string]string
	reads []string
}

func (c *stagedSourceClient) Solve(context.Context, gwclient.SolveRequest) (*gwclient.Result, error) {
	result := gwclient.NewResult()
	result.SetRef(stagedSourceReference{client: c})
	return result, nil
}

type stagedSourceReference struct {
	fakeReference
	client *stagedSourceClient
}

func (r stagedSourceReference) ReadFile(_ context.Context, request gwclient.ReadRequest) ([]byte, error) {
	r.client.reads = append(r.client.reads, request.Filename)
	for stem, body := range r.client.files {
		if request.Filename == path.Join(stagedKitRoot, stem, stagedDescriptorName) {
			return []byte(body), nil
		}
	}
	return nil, fs.ErrNotExist
}

func (r stagedSourceReference) ReadDir(_ context.Context, request gwclient.ReadDirRequest) ([]*fstypes.Stat, error) {
	if request.Path != stagedKitRoot {
		return nil, fs.ErrNotExist
	}
	var stems []string
	for stem := range r.client.files {
		stems = append(stems, stem)
	}
	slices.Sort(stems)
	var entries []*fstypes.Stat
	for _, stem := range stems {
		entries = append(entries, &fstypes.Stat{Path: path.Join(stagedKitRoot, stem), Mode: uint32(os.ModeDir | 0o755)})
	}
	return entries, nil
}
