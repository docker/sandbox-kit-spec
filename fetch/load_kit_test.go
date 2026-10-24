package fetch_test

import (
	"reflect"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/fetch"
	"github.com/docker/sandbox-kit-spec/v3/load"
	"github.com/stretchr/testify/require"
)

// Type identity lets downstream APIs share the contract without copying fields
// or adapting loaders, even when they keep the original fetch names.
func TestLoaderAliases(t *testing.T) {
	require.Equal(t, reflect.TypeFor[load.Loader](), reflect.TypeFor[fetch.KitLoader]())
	require.Equal(t, reflect.TypeFor[load.Kit](), reflect.TypeFor[fetch.LoadedKit]())
	require.Equal(t, reflect.TypeFor[load.LayerLoader](), reflect.TypeFor[fetch.LayerLoader]())

	var client fetch.Client
	var loader load.Loader = client.LoadKit
	options := fetch.Options{Loader: loader}
	require.NotNil(t, options.Loader)
}
