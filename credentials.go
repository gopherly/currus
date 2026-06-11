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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// CredentialProvider is the capability interface for reading the user's
// stored registry credentials. Implemented by Docker and Podman engines;
// not available on containerd (which uses config.toml for static auth).
//
// The returned map keys are registry server URLs exactly as stored in the
// user's config file (e.g. "https://index.docker.io/v1/", "ghcr.io").
// Keys are never normalized — callers that need host-based lookup must
// handle scheme stripping and Docker Hub aliases themselves.
//
// Credentials returns all credentials it can resolve. If some registries
// fail (e.g. a credential helper binary is missing), those entries are
// omitted and the remaining entries are returned. Only errors reading the
// config file itself are fatal.
//
// Usage:
//
//	if cp, ok := eng.(currus.CredentialProvider); ok {
//	    creds, err := cp.Credentials(ctx)
//	}
type CredentialProvider interface {
	Credentials(ctx context.Context) (map[string]AuthEntry, error)
}

// AuthEntry holds resolved credentials for a single registry.
// Field names and semantics match Docker's config.json auth entry so that
// callers can serialize directly to a Kubernetes dockerconfigjson secret
// without transformation.
type AuthEntry struct {
	// ServerURL is the registry URL as stored in the credential source
	// (e.g. "https://index.docker.io/v1/", "ghcr.io").
	ServerURL string

	// Username is the registry username. Empty when IdentityToken is set.
	Username string

	// Password is the registry password (called "Secret" in the helper
	// protocol). Empty when IdentityToken is set.
	Password string

	// Auth is the base64-encoded "username:password" string. It is always
	// populated when Username and Password are set (computed if not present
	// in the source).
	Auth string

	// IdentityToken is an OAuth2 refresh token (used by ACR and similar).
	// When set, Username and Password are empty.
	IdentityToken string

	// RegistryToken is a bearer token for registry v2 authentication.
	RegistryToken string
}

// registryConfig is the parsed form of ~/.docker/config.json or
// a containers auth.json file. The JSON keys use Docker's wire format.
//
//nolint:tagliatelle
type registryConfig struct {
	Auths       map[string]rawAuthEntry `json:"auths"`
	CredsStore  string                  `json:"credsStore"`
	CredHelpers map[string]string       `json:"credHelpers"`
}

// rawAuthEntry is an individual entry inside the "auths" map of a registry
// config file. JSON field names follow Docker's wire format.
type rawAuthEntry struct {
	Auth          string `json:"auth"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	IdentityToken string `json:"identitytoken"`
	RegistryToken string `json:"registrytoken"`
}

// registriesConf is the subset of containers/image registries.conf that
// currus reads to discover global credential helpers for Podman.
//
//nolint:tagliatelle
type registriesConf struct {
	CredentialHelpers []string `toml:"credential-helpers"`
}

// authFilePath pairs a credential file path with a flag indicating whether
// it uses the legacy .dockercfg flat-object format (pre-Docker 1.7).
type authFilePath struct {
	path   string
	legacy bool // true for $HOME/.dockercfg which has no "auths" wrapper
}

// credEnv captures the environment variables relevant to credential
// resolution. It is snapshotted at call time by Credentials() so that
// tests can inject values without mutating the process environment.
type credEnv struct {
	homeDir          string // $HOME fallback
	dockerConfig     string // $DOCKER_CONFIG (directory, not file)
	xdgRuntimeDir    string // $XDG_RUNTIME_DIR
	xdgConfigHome    string // $XDG_CONFIG_HOME
	registryAuthFile string // $REGISTRY_AUTH_FILE (exact file path)
}

// snapshotCredEnv reads the relevant environment variables into a credEnv.
func snapshotCredEnv() credEnv {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = ""
	}

	return credEnv{
		homeDir:          homeDir,
		dockerConfig:     os.Getenv("DOCKER_CONFIG"),
		xdgRuntimeDir:    os.Getenv("XDG_RUNTIME_DIR"),
		xdgConfigHome:    os.Getenv("XDG_CONFIG_HOME"),
		registryAuthFile: os.Getenv("REGISTRY_AUTH_FILE"),
	}
}

// dockerConfigFile returns the path to the Docker config.json file.
func dockerConfigFile(env credEnv) string {
	dir := env.dockerConfig
	if dir == "" {
		dir = filepath.Join(env.homeDir, ".docker")
	}

	return filepath.Join(dir, "config.json")
}

// podmanAuthPaths returns the ordered list of auth file paths that Podman
// reads, starting from the most specific (explicit override or primary
// read/write) to the least specific (legacy fallback). All accessible
// files are read and merged; first-found wins per registry key.
//
// If REGISTRY_AUTH_FILE is set it is the sole source (no cascade).
func podmanAuthPaths(env credEnv) []authFilePath {
	// Explicit override: use only this file, bypass the cascade.
	if env.registryAuthFile != "" {
		return []authFilePath{{path: env.registryAuthFile}}
	}

	var paths []authFilePath

	// Primary read/write: $XDG_RUNTIME_DIR/containers/auth.json (Linux tmpfs).
	// This disappears on reboot, but if the user logged in since boot it's here.
	if env.xdgRuntimeDir != "" {
		paths = append(paths, authFilePath{
			path: filepath.Join(env.xdgRuntimeDir, "containers", "auth.json"),
		})
	}

	// $XDG_CONFIG_HOME/containers/auth.json (persistent, read-only fallback).
	xdgConfigHome := env.xdgConfigHome
	if xdgConfigHome == "" {
		xdgConfigHome = filepath.Join(env.homeDir, ".config")
	}
	paths = append(paths, authFilePath{
		path: filepath.Join(xdgConfigHome, "containers", "auth.json"),
	})

	// $HOME/.docker/config.json and $HOME/.dockercfg (legacy, last resort) are
	// both read-only fallbacks; .dockercfg is the pre-Docker-1.7 flat format.
	dockerCfgDir := env.dockerConfig
	if dockerCfgDir == "" {
		dockerCfgDir = filepath.Join(env.homeDir, ".docker")
	}
	paths = append(paths,
		authFilePath{path: filepath.Join(dockerCfgDir, "config.json")},
		authFilePath{path: filepath.Join(env.homeDir, ".dockercfg"), legacy: true},
	)

	return paths
}

// podmanRegistriesConfPaths returns the ordered list of registries.conf
// paths to search for global credential helpers.
func podmanRegistriesConfPaths(env credEnv) []string {
	xdgConfigHome := env.xdgConfigHome
	if xdgConfigHome == "" {
		xdgConfigHome = filepath.Join(env.homeDir, ".config")
	}

	return []string{
		filepath.Join(xdgConfigHome, "containers", "registries.conf"),
		"/etc/containers/registries.conf",
	}
}

// parseAuthFile parses a registry auth file. Returns an empty config (not
// an error) when the file does not exist.
func parseAuthFile(ap authFilePath) (registryConfig, error) {
	data, err := os.ReadFile(ap.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return registryConfig{
				Auths:       map[string]rawAuthEntry{},
				CredHelpers: map[string]string{},
			}, nil
		}

		return registryConfig{}, fmt.Errorf("read auth file %q: %w", ap.path, err)
	}

	if ap.legacy {
		// Legacy .dockercfg: flat JSON object without the {"auths":...} wrapper.
		var auths map[string]rawAuthEntry
		err = json.Unmarshal(data, &auths)
		if err != nil {
			return registryConfig{}, fmt.Errorf("parse legacy auth file %q: %w", ap.path, err)
		}
		if auths == nil {
			auths = map[string]rawAuthEntry{}
		}

		return registryConfig{Auths: auths, CredHelpers: map[string]string{}}, nil
	}

	var cfg registryConfig
	err = json.Unmarshal(data, &cfg)
	if err != nil {
		return registryConfig{}, fmt.Errorf("parse auth file %q: %w", ap.path, err)
	}
	if cfg.Auths == nil {
		cfg.Auths = map[string]rawAuthEntry{}
	}
	if cfg.CredHelpers == nil {
		cfg.CredHelpers = map[string]string{}
	}

	return cfg, nil
}

// parseRegistriesConf parses a containers registries.conf TOML file.
// Returns an empty struct (not an error) when the file does not exist.
func parseRegistriesConf(path string) (registriesConf, error) {
	data, err := os.ReadFile(path) //nolint:gosec // well-known system/user config path
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return registriesConf{}, nil
		}

		return registriesConf{}, fmt.Errorf("read registries.conf %q: %w", path, err)
	}

	var cfg registriesConf
	_, err = toml.Decode(string(data), &cfg)
	if err != nil {
		return registriesConf{}, fmt.Errorf("parse registries.conf %q: %w", path, err)
	}

	return cfg, nil
}

// helperList invokes `docker-credential-<suffix> list` and returns the
// serverURL → username map. Returns nil map and nil error when the binary is
// not found on PATH.
func helperList(ctx context.Context, logger *slog.Logger, suffix string) (map[string]string, error) {
	name := "docker-credential-" + suffix
	binPath, err := exec.LookPath(name)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			logger.DebugContext(ctx, "credential helper not found; skipping", "helper", name)

			return map[string]string{}, nil
		}

		return nil, fmt.Errorf("look up credential helper %q: %w", name, err)
	}

	cmd := exec.CommandContext(ctx, binPath, "list") //nolint:gosec // binPath resolved via exec.LookPath
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			logger.DebugContext(ctx, "credential helper list failed; skipping",
				"helper", name, "stderr", strings.TrimSpace(string(exitErr.Stderr)))

			return map[string]string{}, nil
		}

		return nil, fmt.Errorf("run %q list: %w", name, err)
	}

	var result map[string]string
	err = json.Unmarshal(out, &result)
	if err != nil {
		return nil, fmt.Errorf("parse %q list output: %w", name, err)
	}

	return result, nil
}

// helperGet invokes `docker-credential-<suffix> get` with serverURL on
// stdin. Returns (entry, true, nil) on success. Returns ({}, false, nil)
// when the binary is not found or reports "credentials not found"
// (non-zero exit). Returns ({}, false, err) on unexpected failure.
func helperGet(ctx context.Context, logger *slog.Logger, suffix, serverURL string) (AuthEntry, bool, error) {
	name := "docker-credential-" + suffix
	binPath, err := exec.LookPath(name)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			logger.DebugContext(ctx, "credential helper not found; skipping",
				"helper", name, "registry", serverURL)

			return AuthEntry{}, false, nil
		}

		return AuthEntry{}, false, fmt.Errorf("look up credential helper %q: %w", name, err)
	}

	cmd := exec.CommandContext(ctx, binPath, "get") //nolint:gosec // binPath resolved via exec.LookPath
	cmd.Stdin = strings.NewReader(serverURL)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Non-zero exit signals "credentials not found" per the protocol.
			logger.DebugContext(ctx, "credential helper get: not found",
				"helper", name, "registry", serverURL)

			return AuthEntry{}, false, nil
		}

		return AuthEntry{}, false, fmt.Errorf("run %q get: %w", name, err)
	}

	// helperResponse matches the wire format of the docker-credential-helpers
	// protocol exactly. The PascalCase JSON keys are mandated by the protocol,
	// not our convention, so tagliatelle is suppressed.
	//nolint:tagliatelle
	var raw struct {
		ServerURL string `json:"ServerURL"`
		Username  string `json:"Username"`
		Secret    string `json:"Secret"`
	}
	err = json.Unmarshal(out, &raw)
	if err != nil {
		return AuthEntry{}, false, fmt.Errorf("parse %q get output: %w", name, err)
	}

	entry := AuthEntry{ServerURL: raw.ServerURL}
	if raw.Username == "<token>" {
		// OAuth2 identity token convention: Username is literal "<token>",
		// Secret is the OAuth2 refresh/identity token.
		entry.IdentityToken = raw.Secret
	} else {
		entry.Username = raw.Username
		entry.Password = raw.Secret
		// Always synthesize the base64 auth field so callers can use the
		// entry directly in a Kubernetes dockerconfigjson secret.
		if raw.Username != "" || raw.Secret != "" {
			entry.Auth = base64.StdEncoding.EncodeToString(
				[]byte(raw.Username + ":" + raw.Secret),
			)
		}
	}

	return entry, true, nil
}

// decodeBase64Credentials decodes a base64-encoded "username:password" pair.
// It splits on the FIRST colon only so passwords containing colons are preserved.
// Returns empty strings if decoding fails or no colon is present.
func decodeBase64Credentials(auth string) (username, password string) {
	decoded, err := base64.StdEncoding.DecodeString(auth)
	if err != nil {
		return "", ""
	}

	before, after, ok := bytes.Cut(decoded, []byte{':'})
	if !ok {
		return "", ""
	}

	return string(before), string(after)
}

// decodeInlineEntry converts a rawAuthEntry (from a JSON auth file) into a
// populated AuthEntry.
func decodeInlineEntry(serverURL string, raw rawAuthEntry) AuthEntry {
	entry := AuthEntry{
		ServerURL:     serverURL,
		IdentityToken: raw.IdentityToken,
		RegistryToken: raw.RegistryToken,
	}

	if raw.Auth != "" {
		entry.Auth = raw.Auth
		entry.Username, entry.Password = decodeBase64Credentials(raw.Auth)
	} else {
		entry.Username = raw.Username
		entry.Password = raw.Password
		if raw.Username != "" || raw.Password != "" {
			entry.Auth = base64.StdEncoding.EncodeToString(
				[]byte(raw.Username + ":" + raw.Password),
			)
		}
	}

	return entry
}

// resolveDockerCredentials resolves all registry credentials using Docker's
// config.json and the Docker credential helper protocol.
//
// Precedence (no fallthrough once a source matches a registry):
//  1. Per-registry credHelpers entry
//  2. Global credsStore
//  3. Inline auths entry
func resolveDockerCredentials(ctx context.Context, logger *slog.Logger, env credEnv) (map[string]AuthEntry, error) {
	configFile := dockerConfigFile(env)
	cfg, err := parseAuthFile(authFilePath{path: configFile})
	if err != nil {
		return nil, fmt.Errorf("docker credentials: %w", err)
	}

	// Collect unique registry keys from all sources.
	registries := make(map[string]struct{})
	for k := range cfg.Auths {
		registries[k] = struct{}{}
	}
	for k := range cfg.CredHelpers {
		registries[k] = struct{}{}
	}
	if cfg.CredsStore != "" {
		listed, listErr := helperList(ctx, logger, cfg.CredsStore)
		if listErr != nil {
			logger.DebugContext(ctx, "credsStore list error; skipping",
				"store", cfg.CredsStore, "err", listErr)
		}
		for k := range listed {
			registries[k] = struct{}{}
		}
	}

	result := make(map[string]AuthEntry, len(registries))
	for reg := range registries {
		entry, ok := resolveDockerRegistry(ctx, logger, reg, cfg)
		if ok {
			result[reg] = entry
		}
	}

	return result, nil
}

// resolveDockerRegistry resolves credentials for a single registry using
// Docker's precedence chain.
func resolveDockerRegistry(
	ctx context.Context,
	logger *slog.Logger,
	registry string,
	cfg registryConfig,
) (AuthEntry, bool) {
	// 1. Per-registry credHelper wins unconditionally.
	if helper, ok := cfg.CredHelpers[registry]; ok {
		entry, found, err := helperGet(ctx, logger, helper, registry)
		if err != nil {
			logger.DebugContext(ctx, "credHelper get error; skipping entry",
				"helper", helper, "registry", registry, "err", err)
		}

		return entry, found // no fallthrough even on error
	}

	// 2. Global credsStore (applies to all registries not in credHelpers).
	if cfg.CredsStore != "" {
		entry, found, err := helperGet(ctx, logger, cfg.CredsStore, registry)
		if err != nil {
			logger.DebugContext(ctx, "credsStore get error; skipping entry",
				"store", cfg.CredsStore, "registry", registry, "err", err)
		}

		return entry, found // no fallthrough even on error
	}

	// 3. Inline auths fallback.
	raw, ok := cfg.Auths[registry]
	if !ok {
		return AuthEntry{}, false
	}
	// An empty entry like {} only marks "helper knows this registry"; it's
	// not a standalone credential when no helper is configured.
	if raw.Auth == "" && raw.Username == "" && raw.IdentityToken == "" && raw.RegistryToken == "" {
		return AuthEntry{}, false
	}

	return decodeInlineEntry(registry, raw), true
}

// mergePodmanAuthFiles reads the Podman auth file cascade and merges all
// entries. First-found per registry key wins across all files.
func mergePodmanAuthFiles(
	ctx context.Context,
	logger *slog.Logger,
	env credEnv,
) (auths map[string]rawAuthEntry, helpers map[string]string) {
	auths = make(map[string]rawAuthEntry)
	helpers = make(map[string]string)
	for _, ap := range podmanAuthPaths(env) {
		cfg, err := parseAuthFile(ap)
		if err != nil {
			logger.DebugContext(ctx, "skip unreadable auth file", "path", ap.path, "err", err)

			continue
		}
		for k, v := range cfg.Auths {
			if _, exists := auths[k]; !exists {
				auths[k] = v
			}
		}
		for k, v := range cfg.CredHelpers {
			if _, exists := helpers[k]; !exists {
				helpers[k] = v
			}
		}
	}

	return auths, helpers
}

// collectPodmanRegistries builds the unique set of registry keys from inline
// auths, per-registry credHelpers, and global external helpers.
func collectPodmanRegistries(
	ctx context.Context,
	logger *slog.Logger,
	auths map[string]rawAuthEntry,
	helpers map[string]string,
	globalHelpers []string,
) map[string]struct{} {
	registries := make(map[string]struct{})
	for k := range auths {
		registries[k] = struct{}{}
	}
	for k := range helpers {
		registries[k] = struct{}{}
	}
	for _, helper := range globalHelpers {
		listed, err := helperList(ctx, logger, helper)
		if err != nil {
			logger.DebugContext(ctx, "global helper list error; skipping",
				"helper", helper, "err", err)

			continue
		}
		for k := range listed {
			registries[k] = struct{}{}
		}
	}

	return registries
}

// resolvePodmanCredentials resolves all registry credentials using Podman's
// cascading auth file chain and registries.conf global helpers.
//
// Precedence per registry (first match wins):
//  1. Per-registry credHelpers entry in any auth file
//  2. External helpers listed in registries.conf (tried in order)
//  3. Inline auths from the merged auth file cascade
func resolvePodmanCredentials(ctx context.Context, logger *slog.Logger, env credEnv) (map[string]AuthEntry, error) {
	mergedAuths, mergedHelpers := mergePodmanAuthFiles(ctx, logger, env)
	globalHelpers := podmanExternalHelpers(env)
	registries := collectPodmanRegistries(ctx, logger, mergedAuths, mergedHelpers, globalHelpers)

	mergedCfg := registryConfig{Auths: mergedAuths, CredHelpers: mergedHelpers}
	result := make(map[string]AuthEntry, len(registries))
	for reg := range registries {
		entry, ok := resolvePodmanRegistry(ctx, logger, reg, mergedCfg, globalHelpers)
		if ok {
			result[reg] = entry
		}
	}

	return result, nil
}

// podmanExternalHelpers reads registries.conf and returns the non-sentinel
// credential helpers. The sentinel "containers-auth.json" is filtered out
// (it means "use the auth files", which is handled separately).
func podmanExternalHelpers(env credEnv) []string {
	for _, path := range podmanRegistriesConfPaths(env) {
		cfg, err := parseRegistriesConf(path)
		if err != nil {
			continue
		}
		var helpers []string
		for _, h := range cfg.CredentialHelpers {
			if h != "containers-auth.json" {
				helpers = append(helpers, h)
			}
		}
		if len(helpers) > 0 {
			return helpers
		}
	}

	return nil
}

// resolvePodmanRegistry resolves credentials for a single registry using
// Podman's precedence chain.
func resolvePodmanRegistry(
	ctx context.Context,
	logger *slog.Logger,
	registry string,
	cfg registryConfig,
	globalHelpers []string,
) (AuthEntry, bool) {
	// 1. Per-registry credHelper from any auth file.
	if helper, ok := cfg.CredHelpers[registry]; ok {
		entry, found, err := helperGet(ctx, logger, helper, registry)
		if err != nil {
			logger.DebugContext(ctx, "credHelper get error",
				"helper", helper, "registry", registry, "err", err)
		}
		if found {
			return entry, true
		}
		// Unlike Docker, Podman falls through on credHelper miss.
	}

	// 2. Global external helpers from registries.conf (tried in order).
	for _, helper := range globalHelpers {
		entry, found, err := helperGet(ctx, logger, helper, registry)
		if err != nil {
			logger.DebugContext(ctx, "global helper get error",
				"helper", helper, "registry", registry, "err", err)

			continue
		}
		if found {
			return entry, true
		}
	}

	// 3. Inline auths fallback.
	raw, ok := cfg.Auths[registry]
	if !ok {
		return AuthEntry{}, false
	}
	if raw.Auth == "" && raw.Username == "" && raw.IdentityToken == "" && raw.RegistryToken == "" {
		return AuthEntry{}, false
	}

	return decodeInlineEntry(registry, raw), true
}
