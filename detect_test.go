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
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildEngineConfig covers the option application and default-filling
// logic of buildEngineConfig.
func TestBuildEngineConfig(t *testing.T) {
	t.Parallel()

	t.Run("nil options are skipped", func(t *testing.T) {
		t.Parallel()
		cfg := buildEngineConfig([]Option{nil, WithEngine(Docker), nil})
		assert.Equal(t, Docker, cfg.kind)
	})

	t.Run("nil options slice produces default logger", func(t *testing.T) {
		t.Parallel()
		cfg := buildEngineConfig(nil)
		assert.NotNil(t, cfg.logger)
	})

	t.Run("supplied logger is preserved", func(t *testing.T) {
		t.Parallel()
		lg := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := buildEngineConfig([]Option{WithLogger(lg)})
		assert.Same(t, lg, cfg.logger)
	})
}

// TestEngineKindFromEnv exercises the environment-variable-driven engine
// selection. This test does not call t.Parallel because t.Setenv is used.
func TestEngineKindFromEnv(t *testing.T) {
	tests := []struct {
		value string
		want  EngineKind
	}{
		{"docker", Docker},
		{"podman", Podman},
		{"containerd", Containerd},
		{"DOCKER", ""},
		{"", ""},
		{"invalid", ""},
	}

	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("CONTAINER_ENGINE", tc.value)
			assert.Equalf(t, tc.want, engineKindFromEnv(), "CONTAINER_ENGINE=%q", tc.value)
		})
	}
}

// TestEndpointTLS covers the three cases of endpointTLS.
func TestEndpointTLS(t *testing.T) {
	t.Parallel()

	t.Run("nil endpoint returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, endpointTLS(nil))
	})

	t.Run("endpoint with nil TLS returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, endpointTLS(&Endpoint{}))
	})

	t.Run("endpoint with TLS returns same pointer", func(t *testing.T) {
		t.Parallel()
		tlsCfg := &TLSConfig{InsecureSkipVerify: true}
		assert.Same(t, tlsCfg, endpointTLS(&Endpoint{TLS: tlsCfg}))
	})
}

// TestPodmanRootlessSocket verifies the XDG_RUNTIME_DIR and home-dir fallback
// paths. This test does not call t.Parallel because t.Setenv is used.
func TestPodmanRootlessSocket(t *testing.T) {
	t.Run("XDG_RUNTIME_DIR set", func(t *testing.T) {
		t.Setenv("XDG_RUNTIME_DIR", "/tmp/xdg-test")
		assert.Equal(t, "/tmp/xdg-test/podman/podman.sock", podmanRootlessSocket())
	})

	t.Run("XDG_RUNTIME_DIR unset falls back to home dir", func(t *testing.T) {
		t.Setenv("XDG_RUNTIME_DIR", "")
		home, err := os.UserHomeDir()
		require.NoErrorf(t, err, "determine home dir %s", home)
		want := home + "/.local/share/containers/podman/machine/podman.sock"
		assert.Equal(t, want, podmanRootlessSocket())
	})
}

// TestOpenKindUnsupported verifies that an unknown EngineKind returns an error
// that wraps ErrUnsupported.
func TestOpenKindUnsupported(t *testing.T) {
	t.Parallel()
	_, err := openKind("bogus", buildEngineConfig(nil))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupported)
}

// TestOpenKindDockerTLSError verifies that openKind surfaces TLS configuration
// errors when connecting to a Docker endpoint.
func TestOpenKindDockerTLSError(t *testing.T) {
	t.Parallel()
	ep := Endpoint{TLS: &TLSConfig{Cert: []byte("not-a-cert"), Key: []byte("not-a-key")}}
	_, err := openKind(Docker, buildEngineConfig([]Option{WithEndpoint(ep)}))
	assert.Error(t, err)
}

// TestOpenKindPodmanTLSError is the same as TestOpenKindDockerTLSError but for
// the Podman path.
func TestOpenKindPodmanTLSError(t *testing.T) {
	t.Parallel()
	ep := Endpoint{TLS: &TLSConfig{Cert: []byte("not-a-cert"), Key: []byte("not-a-key")}}
	_, err := openKind(Podman, buildEngineConfig([]Option{WithEndpoint(ep)}))
	assert.Error(t, err)
}

// TestOpenKindDocker verifies that openKind returns a Docker-kind engine for
// the Docker backend. The moby client is lazy so no daemon is required.
func TestOpenKindDocker(t *testing.T) {
	t.Parallel()
	eng, err := openKind(Docker, buildEngineConfig(nil))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, eng.Close()) })
	assert.Equal(t, Docker, eng.Engine())
}

// TestOpenKindPodman verifies that openKind returns a Podman-kind engine.
func TestOpenKindPodman(t *testing.T) {
	t.Parallel()
	eng, err := openKind(Podman, buildEngineConfig(nil))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, eng.Close()) })
	assert.Equal(t, Podman, eng.Engine())
}

// TestOpenKindContainerd verifies that openKind constructs a Containerd-kind
// engine when given an explicit socket endpoint. A real unix socket listener
// is created so the gRPC client has a valid address.
func TestOpenKindContainerd(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(t.TempDir(), "containerd.sock")
	lc := &net.ListenConfig{}
	l, err := lc.Listen(t.Context(), "unix", sock)
	require.NoErrorf(t, err, "create unix socket %s", sock)
	t.Cleanup(func() { assert.NoError(t, l.Close()) })

	cfg := buildEngineConfig([]Option{WithEndpoint(Endpoint{Host: sock, Namespace: "testns"})})
	eng, err := openKind(Containerd, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, eng.Close()) })
	assert.Equal(t, Containerd, eng.Engine())
}

// TestNewUnsupportedEngine verifies that New with an explicit unknown kind
// returns an error wrapping ErrUnsupported.
func TestNewUnsupportedEngine(t *testing.T) {
	t.Parallel()
	_, err := New(t.Context(), WithEngine("bogus"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupported)
}

// TestMustNewPanic verifies that MustNew panics when no engine can be created.
func TestMustNewPanic(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		MustNew(t.Context(), WithEngine("bogus"))
	})
}
