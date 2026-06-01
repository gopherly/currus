// Copyright 2026 The Gopherly Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package currus

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cerrdefs "github.com/containerd/errdefs"
)

// errUnmappedCtrd is a sentinel used in TestMapCtrdErr to represent an error
// that does not belong to any known containerd error class.
var errUnmappedCtrd = errors.New("unmapped containerd error")

// TestMapCtrdErr covers all branches of the mapCtrdErr translator.
func TestMapCtrdErr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      error
		wantNil bool
		wantIs  error
	}{
		{
			name:    "nil passthrough",
			in:      nil,
			wantNil: true,
		},
		{
			name:   "not found maps to ErrNotFound",
			in:     fmt.Errorf("containerd: %w", cerrdefs.ErrNotFound),
			wantIs: ErrNotFound,
		},
		{
			name:   "already exists maps to ErrAlreadyExists",
			in:     fmt.Errorf("containerd: %w", cerrdefs.ErrAlreadyExists),
			wantIs: ErrAlreadyExists,
		},
		{
			name:   "conflict maps to ErrConflict",
			in:     fmt.Errorf("containerd: %w", cerrdefs.ErrConflict),
			wantIs: ErrConflict,
		},
		{
			name:   "unrecognised error passes through unchanged",
			in:     errUnmappedCtrd,
			wantIs: errUnmappedCtrd,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mapCtrdErr(tc.in)
			if tc.wantNil {
				assert.NoError(t, got)
				return
			}
			assert.ErrorIs(t, got, tc.wantIs)
		})
	}
}

// TestContainerdEngineKind verifies that the driver always reports Containerd
// regardless of how the struct was constructed.
func TestContainerdEngineKind(t *testing.T) {
	t.Parallel()
	e := &containerdEngine{namespace: defaultContainerdNamespace, logger: slog.Default()}
	assert.Equal(t, Containerd, e.Kind())
}

// TestContainerdCapabilities verifies that Capabilities returns the expected
// containerd-specific namespace model.
func TestContainerdCapabilities(t *testing.T) {
	t.Parallel()
	e := &containerdEngine{namespace: defaultContainerdNamespace, logger: slog.Default()}
	assert.Equal(t, "containerd", e.Capabilities().NamespaceModel)
}

// TestCtrdCtx verifies that ctrdCtx injects the engine's namespace into the
// context so that containerd client calls use the correct namespace.
func TestCtrdCtx(t *testing.T) {
	t.Parallel()
	const ns = "currus-test-ns"
	e := &containerdEngine{namespace: ns, logger: slog.Default()}
	got, ok := namespaces.Namespace(e.ctrdCtx(t.Context()))
	require.Truef(t, ok, "namespace not set in context for %s", ns)
	assert.Equal(t, ns, got)
}

// TestNewContainerdEngineDefaults verifies that newContainerdEngine applies
// the expected default namespace and logger when the config fields are empty.
// A real unix socket listener is created so the gRPC client constructor
// receives a valid address without requiring a running containerd daemon.
func TestNewContainerdEngineDefaults(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(t.TempDir(), "containerd.sock")
	lc := &net.ListenConfig{}
	l, err := lc.Listen(t.Context(), "unix", sock)
	require.NoErrorf(t, err, "create unix socket %s", sock)
	t.Cleanup(func() { assert.NoError(t, l.Close()) })

	e, err := newContainerdEngine(containerdConfig{Socket: sock})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, e.Close()) })

	assert.Equal(t, defaultContainerdNamespace, e.namespace)
	assert.NotNil(t, e.logger)
}
