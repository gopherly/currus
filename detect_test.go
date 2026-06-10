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
	"crypto/sha256"
	"encoding/json"
	"fmt"
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
	_, err := openKind(t.Context(), "bogus", buildEngineConfig(nil))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupported)
}

// TestOpenKindDockerTLSError verifies that openKind surfaces TLS configuration
// errors when connecting to a Docker endpoint.
func TestOpenKindDockerTLSError(t *testing.T) {
	t.Parallel()
	ep := Endpoint{TLS: &TLSConfig{Cert: []byte("not-a-cert"), Key: []byte("not-a-key")}}
	_, err := openKind(t.Context(), Docker, buildEngineConfig([]Option{WithEndpoint(ep)}))
	assert.Error(t, err)
}

// TestOpenKindPodmanTLSError is the same as TestOpenKindDockerTLSError but for
// the Podman path.
func TestOpenKindPodmanTLSError(t *testing.T) {
	t.Parallel()
	ep := Endpoint{TLS: &TLSConfig{Cert: []byte("not-a-cert"), Key: []byte("not-a-key")}}
	_, err := openKind(t.Context(), Podman, buildEngineConfig([]Option{WithEndpoint(ep)}))
	assert.Error(t, err)
}

// TestOpenKindDocker verifies that openKind returns a Docker-kind engine.
// This test is daemon-adaptive: when no daemon is reachable, the resolveInfo
// step will fail with ErrDaemonInfo and the test verifies that error.
func TestOpenKindDocker(t *testing.T) {
	t.Parallel()
	eng, err := openKind(t.Context(), Docker, buildEngineConfig(nil))
	if err != nil {
		assert.ErrorIs(t, err, ErrDaemonInfo)
		return
	}
	t.Cleanup(func() { assert.NoError(t, eng.Close()) })
	assert.Equal(t, Docker, eng.Kind())
}

// TestOpenKindPodman verifies that openKind returns a Podman-kind engine.
// This test is daemon-adaptive: when no daemon is reachable, the resolveInfo
// step will fail with ErrDaemonInfo and the test verifies that error.
func TestOpenKindPodman(t *testing.T) {
	t.Parallel()
	eng, err := openKind(t.Context(), Podman, buildEngineConfig(nil))
	if err != nil {
		assert.ErrorIs(t, err, ErrDaemonInfo)
		return
	}
	t.Cleanup(func() { assert.NoError(t, eng.Close()) })
	assert.Equal(t, Podman, eng.Kind())
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
	eng, err := openKind(t.Context(), Containerd, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, eng.Close()) })
	assert.Equal(t, Containerd, eng.Kind())
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

// TestNewViaEnvEngine verifies the CONTAINER_ENGINE env-var path.
// This test is daemon-adaptive: when no Docker daemon is reachable, the
// resolveInfo step fails with ErrDaemonInfo and the test verifies that error.
func TestNewViaEnvEngine(t *testing.T) {
	t.Setenv("CONTAINER_ENGINE", "docker")
	eng, err := New(t.Context())
	if err != nil {
		assert.ErrorIs(t, err, ErrDaemonInfo)
		return
	}
	t.Cleanup(func() { assert.NoError(t, eng.Close()) })
	assert.Equal(t, Docker, eng.Kind())
}

// TestNewAutoDetect is environment-adaptive: if a daemon is reachable
// autoDetect returns an engine; otherwise it returns ErrNoEngine.
func TestNewAutoDetect(t *testing.T) {
	t.Setenv("CONTAINER_ENGINE", "")
	eng, err := New(t.Context())
	if err != nil {
		assert.ErrorIs(t, err, ErrNoEngine)
		return
	}
	t.Cleanup(func() { assert.NoError(t, eng.Close()) })
	assert.NotNil(t, eng)
}

// TestDockerDesktopSocket verifies the path is rooted at the home directory.
func TestDockerDesktopSocket(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.Equal(t, home+"/.docker/run/docker.sock", dockerDesktopSocket())
}

// TestDockerConfigDir verifies DOCKER_CONFIG takes priority over the default.
func TestDockerConfigDir(t *testing.T) {
	t.Run("DOCKER_CONFIG set", func(t *testing.T) {
		t.Setenv("DOCKER_CONFIG", "/custom/docker")
		assert.Equal(t, "/custom/docker", dockerConfigDir())
	})

	t.Run("DOCKER_CONFIG unset uses home dir", func(t *testing.T) {
		t.Setenv("DOCKER_CONFIG", "")
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(home, ".docker"), dockerConfigDir())
	})
}

// TestActiveContextName covers reading and ignoring the active context field.
func TestActiveContextName(t *testing.T) {
	t.Parallel()

	t.Run("present non-default context", func(t *testing.T) {
		t.Parallel()
		dir := writeDockerConfig(t, `{"currentContext":"lima-docker"}`)
		assert.Equal(t, "lima-docker", activeContextName(dir))
	})

	t.Run("currentContext is default", func(t *testing.T) {
		t.Parallel()
		dir := writeDockerConfig(t, `{"currentContext":"default"}`)
		assert.Emptyf(t, activeContextName(dir), "expected empty name for 'default' context")
	})

	t.Run("currentContext absent", func(t *testing.T) {
		t.Parallel()
		dir := writeDockerConfig(t, `{}`)
		assert.Emptyf(t, activeContextName(dir), "expected empty name when currentContext absent")
	})

	t.Run("config.json missing", func(t *testing.T) {
		t.Parallel()
		assert.Emptyf(t, activeContextName(t.TempDir()), "expected empty name when config.json missing")
	})

	t.Run("config.json malformed", func(t *testing.T) {
		t.Parallel()
		dir := writeDockerConfig(t, `not-json`)
		assert.Emptyf(t, activeContextName(dir), "expected empty name when config.json malformed")
	})

	t.Run("empty configDir", func(t *testing.T) {
		t.Parallel()
		assert.Emptyf(t, activeContextName(""), "expected empty name for empty configDir")
	})
}

// TestContextEndpoint covers reading Docker context metadata.
func TestContextEndpoint(t *testing.T) {
	t.Parallel()

	t.Run("valid context", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeContextMeta(t, dir, "lima-docker", "unix:///tmp/lima.sock")
		got, err := contextEndpoint(dir, "lima-docker")
		require.NoError(t, err)
		assert.Equal(t, "unix:///tmp/lima.sock", got)
	})

	t.Run("missing meta.json returns error", func(t *testing.T) {
		t.Parallel()
		_, err := contextEndpoint(t.TempDir(), "missing-ctx")
		require.Error(t, err)
	})

	t.Run("malformed meta.json returns error", func(t *testing.T) {
		t.Parallel()
		dir := writeRawContextMeta(t, "bad-ctx", []byte("not-json"))
		_, err := contextEndpoint(dir, "bad-ctx")
		require.Error(t, err)
	})

	t.Run("no docker endpoint in metadata returns error", func(t *testing.T) {
		t.Parallel()
		dir := writeRawContextMeta(t, "no-docker", []byte(`{"Endpoints":{}}`))
		_, err := contextEndpoint(dir, "no-docker")
		require.Error(t, err)
	})

	t.Run("empty configDir returns error", func(t *testing.T) {
		t.Parallel()
		_, err := contextEndpoint("", "any")
		require.Error(t, err)
	})
}

// TestDockerTLSFromEnv covers the DOCKER_TLS_VERIFY + DOCKER_CERT_PATH paths.
func TestDockerTLSFromEnv(t *testing.T) {
	t.Run("DOCKER_TLS_VERIFY not set returns nil", func(t *testing.T) {
		t.Setenv("DOCKER_TLS_VERIFY", "")
		got, err := dockerTLSFromEnv()
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("DOCKER_TLS_VERIFY=0 returns nil", func(t *testing.T) {
		t.Setenv("DOCKER_TLS_VERIFY", "0")
		got, err := dockerTLSFromEnv()
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("DOCKER_TLS_VERIFY=1 with certs reads files", func(t *testing.T) {
		certDir := writeTLSFiles(t)
		t.Setenv("DOCKER_TLS_VERIFY", "1")
		t.Setenv("DOCKER_CERT_PATH", certDir)
		got, err := dockerTLSFromEnv()
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, []byte("fake-ca"), got.CACert)
		assert.Equal(t, []byte("fake-cert"), got.Cert)
		assert.Equal(t, []byte("fake-key"), got.Key)
	})

	t.Run("missing ca.pem returns error", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DOCKER_TLS_VERIFY", "1")
		t.Setenv("DOCKER_CERT_PATH", dir)
		_, err := dockerTLSFromEnv()
		require.Error(t, err)
	})

	t.Run("missing cert.pem returns error", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.pem"), []byte("x"), 0o600))
		t.Setenv("DOCKER_TLS_VERIFY", "1")
		t.Setenv("DOCKER_CERT_PATH", dir)
		_, err := dockerTLSFromEnv()
		require.Error(t, err)
	})

	t.Run("missing key.pem returns error", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.pem"), []byte("x"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "cert.pem"), []byte("x"), 0o600))
		t.Setenv("DOCKER_TLS_VERIFY", "1")
		t.Setenv("DOCKER_CERT_PATH", dir)
		_, err := dockerTLSFromEnv()
		require.Error(t, err)
	})
}

// TestEnvEndpoint covers the full envEndpoint resolution logic.
// Tests that involve a fake (non-existent) daemon socket are daemon-adaptive:
// they accept either a successful engine (if a daemon happens to listen there)
// or an ErrDaemonInfo error (no daemon; resolveInfo fails after construction).
func TestEnvEndpoint(t *testing.T) {
	t.Run("none set returns nil nil", func(t *testing.T) {
		clearDockerEnv(t)
		eng, err := envEndpoint(t.Context(), buildEngineConfig(nil))
		require.NoError(t, err)
		assert.Nil(t, eng)
	})

	t.Run("DOCKER_HOST returns Docker engine or ErrDaemonInfo", func(t *testing.T) {
		clearDockerEnv(t)
		t.Setenv("DOCKER_HOST", "unix:///tmp/fake.sock")
		eng, err := envEndpoint(t.Context(), buildEngineConfig(nil))
		if err != nil {
			assert.ErrorIs(t, err, ErrDaemonInfo)
			return
		}
		require.NotNil(t, eng)
		t.Cleanup(func() {
			if closeErr := eng.Close(); closeErr != nil {
				t.Logf("close engine: %v", closeErr)
			}
		})
		assert.Equal(t, Docker, eng.Kind())
	})

	t.Run("CONTAINER_HOST returns Podman engine or ErrDaemonInfo", func(t *testing.T) {
		clearDockerEnv(t)
		t.Setenv("CONTAINER_HOST", "unix:///tmp/fake-podman.sock")
		eng, err := envEndpoint(t.Context(), buildEngineConfig(nil))
		if err != nil {
			assert.ErrorIs(t, err, ErrDaemonInfo)
			return
		}
		require.NotNil(t, eng)
		t.Cleanup(func() {
			if closeErr := eng.Close(); closeErr != nil {
				t.Logf("close engine: %v", closeErr)
			}
		})
		assert.Equal(t, Podman, eng.Kind())
	})

	t.Run("DOCKER_HOST and DOCKER_CONTEXT both set return error", func(t *testing.T) {
		clearDockerEnv(t)
		t.Setenv("DOCKER_HOST", "unix:///tmp/fake.sock")
		t.Setenv("DOCKER_CONTEXT", "my-ctx")
		_, err := envEndpoint(t.Context(), buildEngineConfig(nil))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidSpec)
	})

	t.Run("DOCKER_CONTEXT uses named context metadata", func(t *testing.T) {
		clearDockerEnv(t)
		dir := t.TempDir()
		writeContextMeta(t, dir, "my-ctx", "unix:///tmp/ctx.sock")
		t.Setenv("DOCKER_CONTEXT", "my-ctx")
		t.Setenv("DOCKER_CONFIG", dir)
		eng, err := envEndpoint(t.Context(), buildEngineConfig(nil))
		if err != nil {
			assert.ErrorIs(t, err, ErrDaemonInfo)
			return
		}
		require.NotNil(t, eng)
		t.Cleanup(func() {
			if closeErr := eng.Close(); closeErr != nil {
				t.Logf("close engine: %v", closeErr)
			}
		})
		assert.Equal(t, Docker, eng.Kind())
	})

	t.Run("DOCKER_CONTEXT with missing metadata returns error", func(t *testing.T) {
		clearDockerEnv(t)
		t.Setenv("DOCKER_CONTEXT", "nonexistent")
		t.Setenv("DOCKER_CONFIG", t.TempDir())
		_, err := envEndpoint(t.Context(), buildEngineConfig(nil))
		require.Error(t, err)
	})

	t.Run("active context from config.json", func(t *testing.T) {
		clearDockerEnv(t)
		dir := t.TempDir()
		writeDockerConfigInDir(t, dir, `{"currentContext":"lima-docker"}`)
		writeContextMeta(t, dir, "lima-docker", "unix:///tmp/lima.sock")
		t.Setenv("DOCKER_CONFIG", dir)
		eng, err := envEndpoint(t.Context(), buildEngineConfig(nil))
		if err != nil {
			assert.ErrorIs(t, err, ErrDaemonInfo)
			return
		}
		require.NotNil(t, eng)
		t.Cleanup(func() {
			if closeErr := eng.Close(); closeErr != nil {
				t.Logf("close engine: %v", closeErr)
			}
		})
		assert.Equal(t, Docker, eng.Kind())
	})

	t.Run("active context default is skipped", func(t *testing.T) {
		clearDockerEnv(t)
		dir := writeDockerConfig(t, `{"currentContext":"default"}`)
		t.Setenv("DOCKER_CONFIG", dir)
		eng, err := envEndpoint(t.Context(), buildEngineConfig(nil))
		require.NoError(t, err)
		assert.Nil(t, eng)
	})

	t.Run("active context with broken metadata returns error", func(t *testing.T) {
		clearDockerEnv(t)
		dir := t.TempDir()
		writeDockerConfigInDir(t, dir, `{"currentContext":"broken-ctx"}`)
		writeRawContextMetaInDir(t, dir, "broken-ctx", []byte("not-json"))
		t.Setenv("DOCKER_CONFIG", dir)
		_, err := envEndpoint(t.Context(), buildEngineConfig(nil))
		require.Error(t, err)
	})

	t.Run("DOCKER_HOST with TLS reads certs", func(t *testing.T) {
		clearDockerEnv(t)
		certDir := writeTLSFiles(t)
		t.Setenv("DOCKER_HOST", "tcp://docker-host:2376")
		t.Setenv("DOCKER_TLS_VERIFY", "1")
		t.Setenv("DOCKER_CERT_PATH", certDir)
		// The fake PEM data is not valid; tlsConfigFromCurrus rejects it with
		// ErrInvalidSpec. That error proves TLS env vars were read and the
		// cert files were loaded (file-not-found would surface differently).
		_, err := envEndpoint(t.Context(), buildEngineConfig(nil))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidSpec)
	})
}

// TestNewViaDOCKER_HOST verifies that New() picks up DOCKER_HOST.
// This test is daemon-adaptive: when no daemon listens at the fake socket,
// resolveInfo fails with ErrDaemonInfo.
func TestNewViaDOCKER_HOST(t *testing.T) {
	clearDockerEnv(t)
	t.Setenv("DOCKER_HOST", "unix:///tmp/fake-docker.sock")
	eng, err := New(t.Context())
	if err != nil {
		assert.ErrorIs(t, err, ErrDaemonInfo)
		return
	}
	t.Cleanup(func() {
		if closeErr := eng.Close(); closeErr != nil {
			t.Logf("close engine: %v", closeErr)
		}
	})
	assert.Equal(t, Docker, eng.Kind())
}

// TestNewViaCONTAINER_HOST verifies that New() picks up CONTAINER_HOST.
// This test is daemon-adaptive: when no daemon listens at the fake socket,
// resolveInfo fails with ErrDaemonInfo.
func TestNewViaCONTAINER_HOST(t *testing.T) {
	clearDockerEnv(t)
	t.Setenv("CONTAINER_HOST", "unix:///tmp/fake-podman.sock")
	eng, err := New(t.Context())
	if err != nil {
		assert.ErrorIs(t, err, ErrDaemonInfo)
		return
	}
	t.Cleanup(func() {
		if closeErr := eng.Close(); closeErr != nil {
			t.Logf("close engine: %v", closeErr)
		}
	})
	assert.Equal(t, Podman, eng.Kind())
}

// TestNewDockerHostContextConflict verifies that New() returns ErrInvalidSpec
// when DOCKER_HOST and DOCKER_CONTEXT are both set.
func TestNewDockerHostContextConflict(t *testing.T) {
	clearDockerEnv(t)
	t.Setenv("DOCKER_HOST", "unix:///tmp/fake.sock")
	t.Setenv("DOCKER_CONTEXT", "my-ctx")
	_, err := New(t.Context())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidSpec)
}

// clearDockerEnv resets all Docker and Podman env vars that envEndpoint reads
// so tests start from a clean state.
func clearDockerEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"DOCKER_HOST", "DOCKER_CONTEXT",
		"DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH",
		"CONTAINER_HOST", "CONTAINER_ENGINE",
	} {
		t.Setenv(k, "")
	}
	// Point DOCKER_CONFIG at an empty dir so dockerConfigDir() never reads
	// the runner's real ~/.docker/config.json (which may have an active context).
	t.Setenv("DOCKER_CONFIG", t.TempDir())
}

// writeDockerConfig creates a config.json in a temp directory and returns the
// directory path.
func writeDockerConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	writeDockerConfigInDir(t, dir, content)

	return dir
}

// writeDockerConfigInDir writes content to configDir/config.json.
func writeDockerConfigInDir(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0o600))
}

// writeContextMeta writes a valid meta.json for the named context with the
// given host into the Docker context store under configDir.
func writeContextMeta(t *testing.T, configDir, name, host string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"Name": name,
		"Endpoints": map[string]any{
			"docker": map[string]any{"Host": host},
		},
	})
	require.NoError(t, err)
	writeRawContextMetaInDir(t, configDir, name, raw)
}

// writeRawContextMeta writes raw bytes as the meta.json for name in a fresh
// temp directory and returns that directory.
func writeRawContextMeta(t *testing.T, name string, raw []byte) string {
	t.Helper()
	dir := t.TempDir()
	writeRawContextMetaInDir(t, dir, name, raw)

	return dir
}

// writeRawContextMetaInDir writes raw bytes as the meta.json for name inside
// configDir (computing the sha256-based path).
func writeRawContextMetaInDir(t *testing.T, configDir, name string, raw []byte) {
	t.Helper()
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
	metaDir := filepath.Join(configDir, "contexts", "meta", hash)
	require.NoError(t, os.MkdirAll(metaDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(metaDir, "meta.json"), raw, 0o600))
}

// writeTLSFiles creates ca.pem, cert.pem, and key.pem with stub content in a
// temp directory and returns the directory path.
func writeTLSFiles(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.pem"), []byte("fake-ca"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cert.pem"), []byte("fake-cert"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "key.pem"), []byte("fake-key"), 0o600))

	return dir
}
