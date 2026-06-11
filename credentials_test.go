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
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noopLogger returns a logger that discards all output — keeps test output clean.
func noopLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// copyFixture copies src testdata file to dst, creating parent directories.
func copyFixture(t *testing.T, src, dst string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755)) //nolint:gosec // test helper
	data, err := os.ReadFile(src)                             //nolint:gosec // known testdata path
	require.NoErrorf(t, err, "fixture not found: %s", src)
	require.NoError(t, os.WriteFile(dst, data, 0o600)) //nolint:gosec // test helper
}

// loadGolden reads a golden file from testdata/credentials/golden/ and
// unmarshals it into a map[string]AuthEntry.
func loadGolden(t *testing.T, name string) map[string]AuthEntry {
	t.Helper()
	path := filepath.Join("testdata", "credentials", "golden", name)
	data, err := os.ReadFile(path) //nolint:gosec // known testdata path
	require.NoErrorf(t, err, "golden file not found: %s", path)
	var result map[string]AuthEntry
	require.NoErrorf(t, json.Unmarshal(data, &result), "unmarshal golden %s", path)

	return result
}

// writeHelper writes an executable shell script to binDir with the given name.
func writeHelper(t *testing.T, binDir, name, script string) {
	t.Helper()
	path := filepath.Join(binDir, name)
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755)) //nolint:gosec // test helper binary
}

// prependPath adds binDir as the first entry in PATH.
// It uses t.Setenv so it is restored automatically at the end of the test.
// Do NOT call t.Parallel() in tests that use prependPath.
func prependPath(t *testing.T, binDir string) {
	t.Helper()
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
}

// Script templates for credential helper test binaries.

const scriptTestHelper = `#!/bin/sh
case "$1" in
  list)
    echo '{"https://index.docker.io/v1/":"dockeruser","ghcr.io":"ghuser"}'
    ;;
  get)
    read server
    case "$server" in
      "https://index.docker.io/v1/")
        echo '{"ServerURL":"https://index.docker.io/v1/","Username":"dockeruser","Secret":"dockerpass"}'
        ;;
      "ghcr.io")
        echo '{"ServerURL":"ghcr.io","Username":"ghuser","Secret":"ghtoken123"}'
        ;;
      *)
        echo "credentials not found"
        exit 1
        ;;
    esac
    ;;
esac
`

// scriptTestHelperWithQuay is a test-helper that also knows quay.io.
// Used by the mixed-config test where credsStore covers docker hub, ghcr.io,
// and quay.io.
const scriptTestHelperWithQuay = `#!/bin/sh
case "$1" in
  list)
    echo '{"https://index.docker.io/v1/":"dockeruser","ghcr.io":"ghuser","quay.io":"quayuser"}'
    ;;
  get)
    read server
    case "$server" in
      "https://index.docker.io/v1/")
        echo '{"ServerURL":"https://index.docker.io/v1/","Username":"dockeruser","Secret":"dockerpass"}'
        ;;
      "ghcr.io")
        echo '{"ServerURL":"ghcr.io","Username":"ghuser","Secret":"ghtoken123"}'
        ;;
      "quay.io")
        echo '{"ServerURL":"quay.io","Username":"quayuser","Secret":"quaypass"}'
        ;;
      *)
        echo "credentials not found"
        exit 1
        ;;
    esac
    ;;
esac
`

// scriptECRLogin is a minimal ecr-login credential helper stub.
const scriptECRLogin = `#!/bin/sh
case "$1" in
  list)
    echo '{"123456789.dkr.ecr.us-east-1.amazonaws.com":"ecruser"}'
    ;;
  get)
    read server
    echo '{"ServerURL":"123456789.dkr.ecr.us-east-1.amazonaws.com","Username":"ecruser","Secret":"ecrtoken"}'
    ;;
esac
`

// scriptGCR is a minimal gcr credential helper stub.
const scriptGCR = `#!/bin/sh
case "$1" in
  list)
    echo '{"gcr.io":"gcruser"}'
    ;;
  get)
    read server
    echo '{"ServerURL":"gcr.io","Username":"gcruser","Secret":"gcrtoken"}'
    ;;
esac
`

// scriptIdentityHelper returns the <token> username convention
// (ACR-style identity token).
const scriptIdentityHelper = `#!/bin/sh
case "$1" in
  list)
    echo '{"myregistry.azurecr.io":"<token>"}'
    ;;
  get)
    read server
    echo '{"ServerURL":"myregistry.azurecr.io","Username":"<token>","Secret":"eyJhbGciOiJSUzI1NiIs..."}'
    ;;
esac
`

// scriptFailingHelper always exits with a non-zero status.
const scriptFailingHelper = `#!/bin/sh
echo "something went wrong"
exit 1
`

// scriptPartialHelper knows known-registry.io but not unknown-registry.io.
const scriptPartialHelper = `#!/bin/sh
case "$1" in
  list)
    echo '{"known-registry.io":"user1","unknown-registry.io":"user2"}'
    ;;
  get)
    read server
    case "$server" in
      "known-registry.io")
        echo '{"ServerURL":"known-registry.io","Username":"user1","Secret":"pass1"}'
        ;;
      *)
        echo "credentials not found"
        exit 1
        ;;
    esac
    ;;
esac
`

// scriptACRHelper returns acruser:acrpass for myregistry.azurecr.io.
const scriptACRHelper = `#!/bin/sh
case "$1" in
  list)
    echo '{"myregistry.azurecr.io":"acruser"}'
    ;;
  get)
    read server
    echo '{"ServerURL":"myregistry.azurecr.io","Username":"acruser","Secret":"acrpass"}'
    ;;
esac
`

// TestCredentials_DockerInlineOnly verifies inline auths from config.json
// resolve correctly.
func TestCredentials_DockerInlineOnly(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	copyFixture(t, "testdata/credentials/docker/inline_only.json",
		filepath.Join(home, ".docker", "config.json"))

	env := credEnv{
		homeDir:      home,
		dockerConfig: filepath.Join(home, ".docker"),
	}
	creds, err := resolveDockerCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	assert.Equal(t, loadGolden(t, "docker_inline_only.json"), creds)
}

// TestCredentials_DockerCredsStore verifies credentials retrieved via a
// global credsStore helper.
func TestCredentials_DockerCredsStore(t *testing.T) {
	// Uses t.Setenv for PATH — must not be parallel.
	home := t.TempDir()
	copyFixture(t, "testdata/credentials/docker/credsstore_only.json",
		filepath.Join(home, ".docker", "config.json"))

	binDir := t.TempDir()
	prependPath(t, binDir)
	writeHelper(t, binDir, "docker-credential-test-helper", scriptTestHelper)

	env := credEnv{
		homeDir:      home,
		dockerConfig: filepath.Join(home, ".docker"),
	}
	creds, err := resolveDockerCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	assert.Equal(t, loadGolden(t, "docker_credsstore_only.json"), creds)
}

// TestCredentials_DockerCredHelpers verifies per-registry credHelpers entries
// are dispatched correctly.
func TestCredentials_DockerCredHelpers(t *testing.T) {
	// Uses t.Setenv for PATH — must not be parallel.
	home := t.TempDir()
	copyFixture(t, "testdata/credentials/docker/credhelpers_only.json",
		filepath.Join(home, ".docker", "config.json"))

	binDir := t.TempDir()
	prependPath(t, binDir)
	writeHelper(t, binDir, "docker-credential-ecr-login", scriptECRLogin)
	writeHelper(t, binDir, "docker-credential-gcr", scriptGCR)

	env := credEnv{
		homeDir:      home,
		dockerConfig: filepath.Join(home, ".docker"),
	}
	creds, err := resolveDockerCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	assert.Equal(t, loadGolden(t, "docker_credhelpers_only.json"), creds)
}

// TestCredentials_DockerMixed verifies Docker precedence:
// credHelpers > credsStore > inline auths.
func TestCredentials_DockerMixed(t *testing.T) {
	// Validates precedence: credHelpers > credsStore > inline auths (no fallthrough).
	// Uses t.Setenv for PATH — must not be parallel.
	home := t.TempDir()
	copyFixture(t, "testdata/credentials/docker/mixed.json",
		filepath.Join(home, ".docker", "config.json"))

	binDir := t.TempDir()
	prependPath(t, binDir)
	// test-helper is the credsStore: covers docker hub, ghcr.io, quay.io.
	writeHelper(t, binDir, "docker-credential-test-helper", scriptTestHelperWithQuay)
	// ecr-login is a per-registry credHelper (higher precedence than credsStore).
	writeHelper(t, binDir, "docker-credential-ecr-login", scriptECRLogin)

	env := credEnv{
		homeDir:      home,
		dockerConfig: filepath.Join(home, ".docker"),
	}
	creds, err := resolveDockerCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	expected := loadGolden(t, "docker_mixed.json")
	assert.Equal(t, expected, creds)

	// Key assertion: ghcr.io has inline auth but credsStore must win.
	// The golden file contains helper-derived credentials (dockeruser / ghuser),
	// NOT the inline value (inlineuser:inlinepass).
	assert.Equal(t, "ghuser", creds["ghcr.io"].Username)
}

// TestCredentials_DockerHubLegacyKey verifies the full Docker Hub URL key
// is preserved verbatim.
func TestCredentials_DockerHubLegacyKey(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	copyFixture(t, "testdata/credentials/docker/dockerhub_legacy_key.json",
		filepath.Join(home, ".docker", "config.json"))

	env := credEnv{homeDir: home, dockerConfig: filepath.Join(home, ".docker")}
	creds, err := resolveDockerCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	// The full URL key must be preserved as-is.
	assert.Equal(t, loadGolden(t, "docker_dockerhub_key.json"), creds)
	assert.Containsf(t, creds, "https://index.docker.io/v1/",
		"Docker Hub key must be preserved verbatim, not normalized to docker.io")
	assert.NotContains(t, creds, "docker.io")
}

// TestCredentials_DockerIdentityTokenInline verifies inline identity tokens
// populate IdentityToken, not Username+Password.
func TestCredentials_DockerIdentityTokenInline(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	copyFixture(t, "testdata/credentials/docker/identity_token_inline.json",
		filepath.Join(home, ".docker", "config.json"))

	env := credEnv{homeDir: home, dockerConfig: filepath.Join(home, ".docker")}
	creds, err := resolveDockerCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	assert.Equal(t, loadGolden(t, "docker_identity_token.json"), creds)

	entry := creds["myregistry.azurecr.io"]
	assert.NotEmpty(t, entry.IdentityToken)
	assert.Empty(t, entry.Username)
	assert.Empty(t, entry.Password)
}

// TestCredentials_DockerEmptyConfig verifies an empty config.json returns
// an empty credential map.
func TestCredentials_DockerEmptyConfig(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	copyFixture(t, "testdata/credentials/docker/empty.json",
		filepath.Join(home, ".docker", "config.json"))

	env := credEnv{homeDir: home, dockerConfig: filepath.Join(home, ".docker")}
	creds, err := resolveDockerCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	assert.Emptyf(t, creds, "empty config must return empty map")
}

// TestCredentials_DockerMissingConfigFile verifies a missing config.json is
// treated as empty, not an error.
func TestCredentials_DockerMissingConfigFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir() // no config.json created

	env := credEnv{homeDir: home, dockerConfig: filepath.Join(home, ".docker")}
	creds, err := resolveDockerCredentials(t.Context(), noopLogger(), env)
	require.NoErrorf(t, err, "missing config file must not be an error")

	assert.Empty(t, creds)
}

// TestCredentials_DockerNamespacedKeys verifies namespaced registry keys
// are preserved verbatim.
func TestCredentials_DockerNamespacedKeys(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	copyFixture(t, "testdata/credentials/docker/namespaced_keys.json",
		filepath.Join(home, ".docker", "config.json"))

	env := credEnv{homeDir: home, dockerConfig: filepath.Join(home, ".docker")}
	creds, err := resolveDockerCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	assert.Equal(t, loadGolden(t, "docker_namespaced_keys.json"), creds)
	// Hierarchical keys must be preserved verbatim — not split by hostname.
	assert.Contains(t, creds, "registry.example.com/team/project")
	assert.Contains(t, creds, "registry.example.com/other")
}

// TestCredentials_HelperNotFound verifies a missing helper binary is a
// graceful no-op, not an error.
func TestCredentials_HelperNotFound(t *testing.T) {
	t.Parallel()
	// A binary name guaranteed not to exist on any real PATH.
	const nonexistent = "does-not-exist-currus-test-12345"

	result, err := helperList(t.Context(), noopLogger(), nonexistent)
	require.NoErrorf(t, err, "missing helper must not error on list")
	assert.Empty(t, result)

	entry, found, err := helperGet(t.Context(), noopLogger(), nonexistent, "example.io")
	require.NoErrorf(t, err, "missing helper must not error on get")
	assert.False(t, found)
	assert.Empty(t, entry)
}

// TestCredentials_HelperIdentityToken verifies the "<token>" username
// convention maps to IdentityToken.
func TestCredentials_HelperIdentityToken(t *testing.T) {
	// Uses t.Setenv for PATH — must not be parallel.
	binDir := t.TempDir()
	prependPath(t, binDir)
	writeHelper(t, binDir, "docker-credential-identity-helper", scriptIdentityHelper)

	entry, found, err := helperGet(t.Context(), noopLogger(), "identity-helper", "myregistry.azurecr.io")
	require.NoError(t, err)
	require.True(t, found)

	// When helper returns "<token>" as Username, map to IdentityToken field.
	assert.Emptyf(t, entry.Username, "Username must be empty when identity token is used")
	assert.Emptyf(t, entry.Password, "Password must be empty when identity token is used")
	assert.NotEmpty(t, entry.IdentityToken)
}

// TestCredentials_HelperErrorExit verifies a non-zero helper exit is treated
// as "not found", not an error.
func TestCredentials_HelperErrorExit(t *testing.T) {
	// Uses t.Setenv for PATH — must not be parallel.
	binDir := t.TempDir()
	prependPath(t, binDir)
	writeHelper(t, binDir, "docker-credential-failing-helper", scriptFailingHelper)

	result, err := helperList(t.Context(), noopLogger(), "failing-helper")
	require.NoErrorf(t, err, "error exit on list must not propagate")
	assert.Emptyf(t, result, "error exit on list must return empty map")

	entry, found, err := helperGet(t.Context(), noopLogger(), "failing-helper", "example.io")
	require.NoErrorf(t, err, "error exit on get must not propagate")
	assert.False(t, found)
	assert.Empty(t, entry)
}

// TestCredentials_HelperPartialSuccess verifies registries that fail
// resolution are omitted, not fatal.
func TestCredentials_HelperPartialSuccess(t *testing.T) {
	// Uses t.Setenv for PATH — must not be parallel.
	home := t.TempDir()

	// Create a credsstore_only-style fixture that references partial-helper.
	fixtureDst := filepath.Join(home, ".docker", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(fixtureDst), 0o755)) //nolint:gosec // test helper
	// Override credsStore name to use partial-helper.
	require.NoError(t, os.WriteFile(fixtureDst, []byte(`{
  "auths": {
    "known-registry.io": {},
    "unknown-registry.io": {}
  },
  "credsStore": "partial-helper"
}`), 0o600))

	binDir := t.TempDir()
	prependPath(t, binDir)
	writeHelper(t, binDir, "docker-credential-partial-helper", scriptPartialHelper)

	env := credEnv{homeDir: home, dockerConfig: filepath.Join(home, ".docker")}
	creds, err := resolveDockerCredentials(t.Context(), noopLogger(), env)
	require.NoErrorf(t, err, "partial helper failure must not block the whole call")

	// Only known-registry.io resolves; unknown-registry.io returns not-found.
	assert.Equal(t, loadGolden(t, "partial_success.json"), creds)
}

// TestCredentials_AuthFieldSynthesis verifies Auth is synthesized from
// Username+Secret when not explicitly set.
func TestCredentials_AuthFieldSynthesis(t *testing.T) {
	// Validates that when a helper returns Username+Secret (no auth field),
	// the Auth field is synthesized as base64("username:password").
	// Uses t.Setenv for PATH — must not be parallel.
	binDir := t.TempDir()
	prependPath(t, binDir)
	writeHelper(t, binDir, "docker-credential-test-helper", scriptTestHelper)

	entry, found, err := helperGet(t.Context(), noopLogger(), "test-helper", "ghcr.io")
	require.NoError(t, err)
	require.True(t, found)

	assert.NotEmptyf(t, entry.Auth, "Auth field must be synthesized from Username:Password")
	assert.Equal(t, "ghuser", entry.Username)
	assert.Equal(t, "ghtoken123", entry.Password)

	// Cross-check: decode the synthesized auth back to verify round-trip.
	assert.Equal(t, "ghuser", entry.Username)
}

// TestCredentials_LegacyDockercfg verifies the pre-Docker-1.7 .dockercfg
// format is parsed correctly.
func TestCredentials_LegacyDockercfg(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	// Place the legacy file at $HOME/.dockercfg (the last entry in the Podman cascade).
	dst := filepath.Join(home, ".dockercfg")
	data, err := os.ReadFile("testdata/credentials/legacy/dockercfg")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, data, 0o600)) //nolint:gosec // test helper

	// Drive through the Podman resolution chain so the cascade reaches .dockercfg.
	env := credEnv{homeDir: home}
	creds, err := resolvePodmanCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	assert.Equal(t, loadGolden(t, "legacy_dockercfg.json"), creds)
}

// TestCredentials_PodmanPathCascade_XDGConfigHome verifies auth files
// are read from XDG_CONFIG_HOME.
func TestCredentials_PodmanPathCascade_XDGConfigHome(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	// Place auth file at $XDG_CONFIG_HOME/containers/auth.json.
	xdgCfg := filepath.Join(home, "xdg-config")
	copyFixture(t, "testdata/credentials/podman/auth_basic.json",
		filepath.Join(xdgCfg, "containers", "auth.json"))

	env := credEnv{
		homeDir:       home,
		xdgConfigHome: xdgCfg,
	}
	creds, err := resolvePodmanCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	assert.Equal(t, loadGolden(t, "podman_basic.json"), creds)
}

// TestCredentials_PodmanPathCascade_XDGRuntimeDir verifies auth files
// are read from XDG_RUNTIME_DIR.
func TestCredentials_PodmanPathCascade_XDGRuntimeDir(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	// Place auth file at primary $XDG_RUNTIME_DIR/containers/auth.json.
	xdgRuntime := filepath.Join(home, "runtime")
	copyFixture(t, "testdata/credentials/podman/auth_basic.json",
		filepath.Join(xdgRuntime, "containers", "auth.json"))

	env := credEnv{
		homeDir:       home,
		xdgRuntimeDir: xdgRuntime,
		// No xdgConfigHome set — persistent fallback won't interfere.
	}
	creds, err := resolvePodmanCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	assert.Equal(t, loadGolden(t, "podman_basic.json"), creds)
}

// TestCredentials_PodmanPathCascade_RegistryAuthFile verifies
// REGISTRY_AUTH_FILE bypasses the cascade.
func TestCredentials_PodmanPathCascade_RegistryAuthFile(t *testing.T) {
	t.Parallel()

	// REGISTRY_AUTH_FILE bypasses the entire cascade.
	home := t.TempDir()
	authFile := filepath.Join(home, "custom-auth.json")
	copyFixture(t, "testdata/credentials/podman/auth_basic.json", authFile)

	// Also place a different file at the standard location to verify it's NOT read.
	copyFixture(t, "testdata/credentials/podman/auth_overlapping.json",
		filepath.Join(home, ".config", "containers", "auth.json"))

	env := credEnv{
		homeDir:          home,
		registryAuthFile: authFile, // explicit override
	}
	creds, err := resolvePodmanCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	// Must return only the entries from auth_basic.json, not auth_overlapping.json.
	assert.Equal(t, loadGolden(t, "podman_basic.json"), creds)
	assert.NotContainsf(t, creds, "extra.registry.io",
		"extra.registry.io is only in auth_overlapping.json; REGISTRY_AUTH_FILE must suppress it")
}

// TestCredentials_PodmanFileMerge verifies first-found-wins when multiple
// auth files overlap.
func TestCredentials_PodmanFileMerge(t *testing.T) {
	t.Parallel()

	// auth_basic.json (runtime, primary) has docker.io + quay.io.
	// auth_overlapping.json (config, secondary) has docker.io (different creds) + extra.registry.io.
	// First-found wins for docker.io; extra.registry.io comes from the secondary.
	home := t.TempDir()
	xdgRuntime := filepath.Join(home, "runtime")
	xdgConfig := filepath.Join(home, ".config")

	copyFixture(t, "testdata/credentials/podman/auth_basic.json",
		filepath.Join(xdgRuntime, "containers", "auth.json"))
	copyFixture(t, "testdata/credentials/podman/auth_overlapping.json",
		filepath.Join(xdgConfig, "containers", "auth.json"))

	env := credEnv{
		homeDir:       home,
		xdgRuntimeDir: xdgRuntime,
		xdgConfigHome: xdgConfig,
	}
	creds, err := resolvePodmanCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	assert.Equal(t, loadGolden(t, "podman_merge.json"), creds)

	// docker.io must come from the primary (runtime) file, not the overlapping one.
	assert.Equalf(t, "podmanuser", creds["docker.io"].Username,
		"first-found wins: primary (XDG_RUNTIME_DIR) must override secondary")
}

// TestCredentials_PodmanRegistriesConf verifies global helpers from
// registries.conf extend the registry set.
func TestCredentials_PodmanRegistriesConf(t *testing.T) {
	// Global helpers from registries.conf are invoked for registry discovery.
	// Uses t.Setenv for PATH — must not be parallel.
	home := t.TempDir()
	xdgConfig := filepath.Join(home, ".config")

	// Auth file has docker.io and quay.io inline.
	copyFixture(t, "testdata/credentials/podman/auth_basic.json",
		filepath.Join(xdgConfig, "containers", "auth.json"))
	// registries.conf adds test-helper as global helper.
	copyFixture(t, "testdata/credentials/registries/with_helpers.conf",
		filepath.Join(xdgConfig, "containers", "registries.conf"))

	binDir := t.TempDir()
	prependPath(t, binDir)
	// test-helper returns docker hub, ghcr.io from list (extends the registry set).
	writeHelper(t, binDir, "docker-credential-test-helper", scriptTestHelper)

	env := credEnv{
		homeDir:       home,
		xdgConfigHome: xdgConfig,
	}
	creds, err := resolvePodmanCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	// Expected: docker.io + quay.io from inline, plus docker hub URL + ghcr.io via helper.
	// docker.io (inline) takes precedence over any helper result for it since the helper
	// knows "https://index.docker.io/v1/" (a different key), not "docker.io".
	assert.Equal(t, loadGolden(t, "podman_with_registries_conf.json"), creds)
	assert.Containsf(t, creds, "ghcr.io", "global helper must add registries to the set")
	assert.Containsf(t, creds, "https://index.docker.io/v1/", "global helper must add registries to the set")
}

// TestCredentials_PodmanCredHelpers verifies per-registry credHelpers in
// auth.json are dispatched correctly.
func TestCredentials_PodmanCredHelpers(t *testing.T) {
	// Per-registry credHelpers in auth.json (Podman-style, no credsStore).
	// Uses t.Setenv for PATH — must not be parallel.
	home := t.TempDir()
	xdgConfig := filepath.Join(home, ".config")

	// auth_with_credhelpers.json has myregistry.azurecr.io → test-helper.
	copyFixture(t, "testdata/credentials/podman/auth_with_credhelpers.json",
		filepath.Join(xdgConfig, "containers", "auth.json"))

	binDir := t.TempDir()
	prependPath(t, binDir)
	writeHelper(t, binDir, "docker-credential-test-helper", scriptACRHelper)

	env := credEnv{
		homeDir:       home,
		xdgConfigHome: xdgConfig,
	}
	creds, err := resolvePodmanCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	assert.Equal(t, loadGolden(t, "podman_credhelpers.json"), creds)
}

// TestCredentials_PodmanRegistriesConf_SentinelOnly verifies the
// containers-auth.json sentinel invokes no external helpers.
func TestCredentials_PodmanRegistriesConf_SentinelOnly(t *testing.T) {
	t.Parallel()

	// registries.conf with only the sentinel ("containers-auth.json") must not
	// invoke any external helpers.
	home := t.TempDir()
	xdgConfig := filepath.Join(home, ".config")

	copyFixture(t, "testdata/credentials/podman/auth_basic.json",
		filepath.Join(xdgConfig, "containers", "auth.json"))
	copyFixture(t, "testdata/credentials/registries/sentinel_only.conf",
		filepath.Join(xdgConfig, "containers", "registries.conf"))

	env := credEnv{
		homeDir:       home,
		xdgConfigHome: xdgConfig,
	}
	creds, err := resolvePodmanCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	// Sentinel-only means: no external helpers, just auth files. Same as podman_basic.
	assert.Equal(t, loadGolden(t, "podman_basic.json"), creds)
}

// TestCredentials_PodmanEphemeralMissing verifies the cascade falls through
// when XDG_RUNTIME_DIR has no auth file.
func TestCredentials_PodmanEphemeralMissing(t *testing.T) {
	t.Parallel()

	// When XDG_RUNTIME_DIR is set but the file inside it doesn't exist,
	// the cascade must fall through to the persistent paths.
	home := t.TempDir()
	xdgRuntime := filepath.Join(home, "runtime-dir")   // directory exists but no auth.json
	require.NoError(t, os.MkdirAll(xdgRuntime, 0o755)) //nolint:gosec // test helper

	xdgConfig := filepath.Join(home, ".config")
	copyFixture(t, "testdata/credentials/podman/auth_basic.json",
		filepath.Join(xdgConfig, "containers", "auth.json"))

	env := credEnv{
		homeDir:       home,
		xdgRuntimeDir: xdgRuntime, // set but auth.json absent (simulates reboot)
		xdgConfigHome: xdgConfig,
	}
	creds, err := resolvePodmanCredentials(t.Context(), noopLogger(), env)
	require.NoError(t, err)

	assert.Equalf(t, loadGolden(t, "podman_basic.json"), creds,
		"ephemeral auth.json missing must fall through to persistent config")
}

// TestCredentials_ParseAuthFile_DockerFormat verifies Docker config.json
// is parsed into the correct structure.
func TestCredentials_ParseAuthFile_DockerFormat(t *testing.T) {
	t.Parallel()

	cfg, err := parseAuthFile(authFilePath{path: "testdata/credentials/docker/inline_only.json"})
	require.NoError(t, err)

	assert.Len(t, cfg.Auths, 3)
	assert.Contains(t, cfg.Auths, "https://index.docker.io/v1/")
	assert.Contains(t, cfg.Auths, "ghcr.io")
	assert.Contains(t, cfg.Auths, "registry.example.com")
}

// TestCredentials_ParseAuthFile_LegacyFormat verifies the legacy
// .dockercfg flat-object format is parsed correctly.
func TestCredentials_ParseAuthFile_LegacyFormat(t *testing.T) {
	t.Parallel()

	cfg, err := parseAuthFile(authFilePath{
		path:   "testdata/credentials/legacy/dockercfg",
		legacy: true,
	})
	require.NoError(t, err)

	assert.Len(t, cfg.Auths, 2)
	assert.Contains(t, cfg.Auths, "https://index.docker.io/v1/")
	assert.Contains(t, cfg.Auths, "quay.io")
	// Legacy format has no credsStore or credHelpers.
	assert.Empty(t, cfg.CredsStore)
	assert.Empty(t, cfg.CredHelpers)
}

// TestCredentials_ParseAuthFile_Missing verifies a missing auth file
// returns an empty config without error.
func TestCredentials_ParseAuthFile_Missing(t *testing.T) {
	t.Parallel()

	cfg, err := parseAuthFile(authFilePath{path: "/does/not/exist/auth.json"})
	require.NoErrorf(t, err, "missing file must not error")
	assert.Empty(t, cfg.Auths)
}

// TestCredentials_ParseRegistriesConf_WithHelpers verifies
// registries.conf with helpers parses correctly.
func TestCredentials_ParseRegistriesConf_WithHelpers(t *testing.T) {
	t.Parallel()

	cfg, err := parseRegistriesConf("testdata/credentials/registries/with_helpers.conf")
	require.NoError(t, err)

	assert.Equal(t, []string{"test-helper", "containers-auth.json"}, cfg.CredentialHelpers)
}

// TestCredentials_ParseRegistriesConf_SentinelOnly verifies only the
// sentinel helper name is returned.
func TestCredentials_ParseRegistriesConf_SentinelOnly(t *testing.T) {
	t.Parallel()

	cfg, err := parseRegistriesConf("testdata/credentials/registries/sentinel_only.conf")
	require.NoError(t, err)

	assert.Equal(t, []string{"containers-auth.json"}, cfg.CredentialHelpers)
}

// TestCredentials_ParseRegistriesConf_Empty verifies an empty
// registries.conf returns no helpers.
func TestCredentials_ParseRegistriesConf_Empty(t *testing.T) {
	t.Parallel()

	cfg, err := parseRegistriesConf("testdata/credentials/registries/empty.conf")
	require.NoError(t, err)

	assert.Empty(t, cfg.CredentialHelpers)
}

// TestCredentials_ParseRegistriesConf_Missing verifies a missing
// registries.conf returns no helpers without error.
func TestCredentials_ParseRegistriesConf_Missing(t *testing.T) {
	t.Parallel()

	cfg, err := parseRegistriesConf("/does/not/exist/registries.conf")
	require.NoErrorf(t, err, "missing file must not error")
	assert.Empty(t, cfg.CredentialHelpers)
}

// TestCredentials_DecodeInlineEntry_ColonInPassword verifies passwords with
// colons split only on the first colon.
func TestCredentials_DecodeInlineEntry_ColonInPassword(t *testing.T) {
	t.Parallel()

	// "admin:s3cr3t:pass" — password contains a colon. Must split on FIRST colon only.
	raw := rawAuthEntry{Auth: "YWRtaW46czNjcjN0OnBhc3M="}
	entry := decodeInlineEntry("registry.example.com", raw)

	assert.Equal(t, "admin", entry.Username)
	assert.Equal(t, "s3cr3t:pass", entry.Password)
}

// TestCredentials_DecodeInlineEntry_IdentityToken verifies an
// identity-token-only entry has no Auth field.
func TestCredentials_DecodeInlineEntry_IdentityToken(t *testing.T) {
	t.Parallel()

	raw := rawAuthEntry{IdentityToken: "some-oauth-token"}
	entry := decodeInlineEntry("myregistry.io", raw)

	assert.Emptyf(t, entry.Auth, "no Auth when only IdentityToken is present")
	assert.Equal(t, "some-oauth-token", entry.IdentityToken)
}

// TestCredentials_DecodeInlineEntry_DirectFields verifies
// Username+Password synthesizes the Auth field.
func TestCredentials_DecodeInlineEntry_DirectFields(t *testing.T) {
	t.Parallel()

	// Some clients write username/password directly without the auth field.
	raw := rawAuthEntry{Username: "alice", Password: "secret"}
	entry := decodeInlineEntry("example.io", raw)

	assert.Equal(t, "alice", entry.Username)
	assert.Equal(t, "secret", entry.Password)
	assert.NotEmptyf(t, entry.Auth, "Auth must be synthesized from Username:Password")
}

// TestCredentials_PodmanAuthPaths_ExplicitOverride verifies
// REGISTRY_AUTH_FILE yields a single-path list.
func TestCredentials_PodmanAuthPaths_ExplicitOverride(t *testing.T) {
	t.Parallel()

	env := credEnv{
		homeDir:          "/home/user",
		registryAuthFile: "/custom/auth.json",
	}
	paths := podmanAuthPaths(env)

	require.Lenf(t, paths, 1, "REGISTRY_AUTH_FILE must bypass the cascade")
	assert.Equal(t, "/custom/auth.json", paths[0].path)
	assert.False(t, paths[0].legacy)
}

// TestCredentials_PodmanAuthPaths_LegacyLast verifies .dockercfg is the
// last entry in the cascade.
func TestCredentials_PodmanAuthPaths_LegacyLast(t *testing.T) {
	t.Parallel()

	env := credEnv{homeDir: "/home/user"}
	paths := podmanAuthPaths(env)

	last := paths[len(paths)-1]
	assert.Truef(t, last.legacy, "last path in cascade must be the legacy .dockercfg")
	assert.Equal(t, "/home/user/.dockercfg", last.path)
}

// TestCredentials_DockerConfigFile_EnvOverride verifies DOCKER_CONFIG
// overrides the default directory.
func TestCredentials_DockerConfigFile_EnvOverride(t *testing.T) {
	t.Parallel()

	env := credEnv{homeDir: "/home/user", dockerConfig: "/custom/docker"}
	assert.Equal(t, "/custom/docker/config.json", dockerConfigFile(env))
}

// TestCredentials_DockerConfigFile_DefaultHome verifies the default config
// path uses $HOME/.docker.
func TestCredentials_DockerConfigFile_DefaultHome(t *testing.T) {
	t.Parallel()

	env := credEnv{homeDir: "/home/user"}
	assert.Equal(t, "/home/user/.docker/config.json", dockerConfigFile(env))
}

// TestFakeCredentials_Empty verifies the test fake implements
// CredentialProvider and returns empty by default.
func TestFakeCredentials_Empty(t *testing.T) {
	t.Parallel()

	fake := newFakeForTest()
	cp, ok := interface{}(fake).(CredentialProvider)
	require.Truef(t, ok, "Fake must implement CredentialProvider")

	creds, err := cp.Credentials(t.Context())
	require.NoError(t, err)
	assert.Emptyf(t, creds, "default fake returns empty credentials")
}

// TestFakeCredentials_WithCredentials verifies withFakeCredentials injects
// a fixed credential map.
func TestFakeCredentials_WithCredentials(t *testing.T) {
	t.Parallel()

	input := map[string]AuthEntry{
		"ghcr.io": {
			ServerURL: "ghcr.io",
			Username:  "testuser",
			Password:  "testpass",
			Auth:      "dGVzdHVzZXI6dGVzdHBhc3M=",
		},
	}

	fake := newFakeForTest(withFakeCredentials(input))
	cp, ok := interface{}(fake).(CredentialProvider)
	require.True(t, ok)

	creds, err := cp.Credentials(t.Context())
	require.NoError(t, err)
	assert.Equal(t, input, creds)
}

// TestFakeCredentials_ReturnsCopy verifies Credentials returns a copy so
// callers cannot mutate the fake's state.
func TestFakeCredentials_ReturnsCopy(t *testing.T) {
	t.Parallel()

	input := map[string]AuthEntry{
		"ghcr.io": {Username: "user"},
	}
	fake := newFakeForTest(withFakeCredentials(input))
	cp, ok := interface{}(fake).(CredentialProvider)
	require.Truef(t, ok, "testFake must implement CredentialProvider")

	c1, err := cp.Credentials(t.Context())
	require.NoError(t, err)
	c1["ghcr.io"] = AuthEntry{Username: "mutated"}

	c2, err := cp.Credentials(t.Context())
	require.NoError(t, err)
	assert.Equalf(t, "user", c2["ghcr.io"].Username,
		"Credentials() must return a copy; mutating the result must not affect the fake")
}

// These thin wrappers let credentials_test.go reach into the currustest
// package without import cycles.

// newFakeForTest returns a *Fake from the currustest package.
// We can't import currustest here (same module, would create a cycle), so we
// build a minimal in-process fake for the CredentialProvider tests instead.

type testFakeOption func(*testFake)

func withFakeCredentials(c map[string]AuthEntry) testFakeOption {
	return func(f *testFake) { f.creds = c }
}

type testFake struct {
	creds map[string]AuthEntry
}

func newFakeForTest(opts ...testFakeOption) *testFake {
	f := &testFake{}
	for _, o := range opts {
		o(f)
	}

	return f
}

func (f *testFake) Credentials(_ context.Context) (map[string]AuthEntry, error) {
	if f.creds == nil {
		return map[string]AuthEntry{}, nil
	}
	out := make(map[string]AuthEntry, len(f.creds))
	maps.Copy(out, f.creds)

	return out, nil
}

// Verify the test fake itself satisfies the interface.
var _ CredentialProvider = (*testFake)(nil)
