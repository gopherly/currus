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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// sentinel errors used internally by the detect layer; not exported because
// callers have no need to match on them specifically.
var (
	errConfigDirUnknown        = errors.New("docker config directory unknown")
	errNoEndpointInContextMeta = errors.New("no docker endpoint in context metadata")
)

// New creates an Engine by detecting or constructing the appropriate backend.
//
// With no options, New probes endpoints in priority order until a reachable
// engine responds to [Engine.Ping]:
//
//  1. DOCKER_HOST env var (Docker engine; reads DOCKER_TLS_VERIFY and
//     DOCKER_CERT_PATH for TLS)
//  2. CONTAINER_HOST env var (Podman engine)
//  3. DOCKER_CONTEXT env var (reads Docker context metadata)
//  4. Active context from ~/.docker/config.json (skipped when "default" or absent)
//  5. CONTAINER_ENGINE env var ("docker", "podman", or "containerd")
//  6. Docker socket (/var/run/docker.sock, then ~/.docker/run/docker.sock)
//  7. Podman rootless socket ($XDG_RUNTIME_DIR/podman/podman.sock or
//     ~/.local/share/containers/podman/machine/podman.sock)
//  8. Podman rootful socket (/run/podman/podman.sock)
//  9. containerd socket (/run/containerd/containerd.sock)
//
// DOCKER_HOST and DOCKER_CONTEXT are mutually exclusive; setting both returns
// an error wrapping [ErrInvalidSpec].
//
// Use [WithEngine] to skip detection and select a backend explicitly.
// Use [WithEndpoint] to override the default socket path.
func New(ctx context.Context, opts ...Option) (Engine, error) {
	cfg := buildEngineConfig(opts)

	if cfg.kind != "" {
		return openKind(cfg.kind, cfg)
	}

	if eng, err := envEndpoint(cfg); eng != nil || err != nil {
		if err != nil {
			return nil, err
		}

		return eng, nil
	}

	if envKind := engineKindFromEnv(); envKind != "" {
		return openKind(envKind, cfg)
	}

	return autoDetect(ctx, cfg)
}

// MustNew is like [New] but panics if no engine is available.
//
// Intended for package-level initialization or program startup where an
// unavailable engine is a programmer error, not a recoverable condition.
// Do not use MustNew in package code that might be called from tests or
// in contexts where the environment is not controlled.
func MustNew(ctx context.Context, opts ...Option) Engine {
	eng, err := New(ctx, opts...)
	if err != nil {
		panic(fmt.Sprintf("currus.MustNew: %v", err))
	}

	return eng
}

// buildEngineConfig applies all options and returns the resulting engineConfig.
func buildEngineConfig(opts []Option) engineConfig {
	var cfg engineConfig
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}

	return cfg
}

// openKind constructs the engine for the given kind using the provided config.
func openKind(kind EngineKind, cfg engineConfig) (Engine, error) {
	switch kind {
	case Docker:
		host := ""
		if cfg.endpoint != nil {
			host = cfg.endpoint.Host
		}
		tlsCfg, err := tlsConfigFromCurrus(endpointTLS(cfg.endpoint))
		if err != nil {
			return nil, fmt.Errorf("currus: Docker TLS config: %w", err)
		}

		return newDockerEngine(dockerConfig{
			Host:         host,
			DaemonSocket: resolveDaemonSocket(host, cfg.daemonSocket),
			Kind:         dockerKindDocker,
			TLS:          tlsCfg,
			Logger:       cfg.logger,
			Tracer:       cfg.tracer,
		})
	case Podman:
		host := ""
		if cfg.endpoint != nil {
			host = cfg.endpoint.Host
		}
		tlsCfg, err := tlsConfigFromCurrus(endpointTLS(cfg.endpoint))
		if err != nil {
			return nil, fmt.Errorf("currus: Podman TLS config: %w", err)
		}

		return newDockerEngine(dockerConfig{
			Host:         host,
			DaemonSocket: resolveDaemonSocket(host, cfg.daemonSocket),
			Kind:         dockerKindPodman,
			TLS:          tlsCfg,
			Logger:       cfg.logger,
			Tracer:       cfg.tracer,
		})
	case Containerd:
		socket := ""
		ns := ""
		if cfg.endpoint != nil {
			socket = cfg.endpoint.Host
			ns = cfg.endpoint.Namespace
		}

		return newContainerdEngine(containerdConfig{
			Socket:       socket,
			DaemonSocket: resolveDaemonSocket(socket, cfg.daemonSocket),
			Namespace:    ns,
			Logger:       cfg.logger,
			Tracer:       cfg.tracer,
		})
	default:
		return nil, fmt.Errorf("engine kind %q: %w", kind, ErrUnsupported)
	}
}

// autoDetect probes the well-known socket paths in priority order.
func autoDetect(ctx context.Context, cfg engineConfig) (Engine, error) {
	type candidate struct {
		kind   EngineKind
		open   func() (Engine, error)
		socket string
	}

	dockerCandidate := func(socket string, dkind dockerDriverKind, kind EngineKind) candidate {
		host := "unix://" + socket
		return candidate{
			kind:   kind,
			socket: socket,
			open: func() (Engine, error) {
				return newDockerEngine(dockerConfig{
					Host:         host,
					DaemonSocket: resolveDaemonSocket(host, cfg.daemonSocket),
					Kind:         dkind,
					Logger:       cfg.logger,
					Tracer:       cfg.tracer,
				})
			},
		}
	}

	ctrdSocket := defaultContainerdSocket
	candidates := []candidate{
		dockerCandidate(defaultDockerSocket, dockerKindDocker, Docker),
		dockerCandidate(dockerDesktopSocket(), dockerKindDocker, Docker),
		dockerCandidate(podmanRootlessSocket(), dockerKindPodman, Podman),
		dockerCandidate(defaultPodmanRootfulSocket, dockerKindPodman, Podman),
		{
			kind:   Containerd,
			socket: ctrdSocket,
			open: func() (Engine, error) {
				return newContainerdEngine(containerdConfig{
					Socket:       ctrdSocket,
					DaemonSocket: resolveDaemonSocket(ctrdSocket, cfg.daemonSocket),
					Logger:       cfg.logger,
					Tracer:       cfg.tracer,
				})
			},
		},
	}

	for _, c := range candidates {
		if c.socket == "" {
			continue
		}
		eng, err := c.open()
		if err != nil {
			cfg.logger.DebugContext(ctx, "engine candidate skipped (open failed)",
				"kind", c.kind, "socket", c.socket, "err", err)

			continue
		}
		if err = eng.Ping(ctx); err != nil {
			cfg.logger.DebugContext(ctx, "engine candidate skipped (ping failed)",
				"kind", c.kind, "socket", c.socket, "err", err)
			_ = eng.Close() //nolint:errcheck // best-effort close on failed candidate

			continue
		}
		cfg.logger.DebugContext(ctx, "engine detected",
			"kind", c.kind, "socket", c.socket)

		return eng, nil
	}

	return nil, ErrNoEngine
}

// engineKindFromEnv reads the CONTAINER_ENGINE environment variable.
func engineKindFromEnv() EngineKind {
	v := os.Getenv("CONTAINER_ENGINE")
	switch EngineKind(v) {
	case Docker, Podman, Containerd:
		return EngineKind(v)
	default:
		return ""
	}
}

// endpointTLS extracts the TLSConfig from an Endpoint, returning nil if the
// endpoint is nil or has no TLS configuration.
func endpointTLS(ep *Endpoint) *TLSConfig {
	if ep == nil {
		return nil
	}

	return ep.TLS
}

// podmanRootlessSocket returns the best-effort rootless Podman socket path
// for the current user, or "" if it cannot be determined.
func podmanRootlessSocket() string {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return xdg + "/podman/podman.sock"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return home + "/.local/share/containers/podman/machine/podman.sock"
}

// dockerDesktopSocket returns the Docker Desktop socket path on macOS
// (~/.docker/run/docker.sock), or "" if the home directory cannot be determined.
func dockerDesktopSocket() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return home + "/.docker/run/docker.sock"
}

// envEndpoint resolves an Engine from Docker and Podman environment variables
// and the active Docker context. It returns (nil, nil) when no relevant
// variable is set, signaling New to continue to the next detection step.
//
// Resolution order:
//  1. DOCKER_HOST (Docker; reads DOCKER_TLS_VERIFY and DOCKER_CERT_PATH)
//  2. CONTAINER_HOST (Podman)
//  3. DOCKER_CONTEXT (Docker context by name)
//  4. Active context from ~/.docker/config.json
func envEndpoint(cfg engineConfig) (Engine, error) {
	dockerHost := os.Getenv("DOCKER_HOST")
	dockerContext := os.Getenv("DOCKER_CONTEXT")

	if dockerHost != "" && dockerContext != "" {
		return nil, fmt.Errorf("currus: %w: DOCKER_HOST and DOCKER_CONTEXT are mutually exclusive", ErrInvalidSpec)
	}

	if dockerHost != "" {
		currusTLS, err := dockerTLSFromEnv()
		if err != nil {
			return nil, fmt.Errorf("currus: DOCKER_HOST TLS: %w", err)
		}

		tlsCfg, err := tlsConfigFromCurrus(currusTLS)
		if err != nil {
			return nil, fmt.Errorf("currus: DOCKER_HOST TLS: %w", err)
		}

		return newDockerEngine(dockerConfig{
			Host:         dockerHost,
			DaemonSocket: resolveDaemonSocket(dockerHost, cfg.daemonSocket),
			Kind:         dockerKindDocker,
			TLS:          tlsCfg,
			Logger:       cfg.logger,
			Tracer:       cfg.tracer,
		})
	}

	if containerHost := os.Getenv("CONTAINER_HOST"); containerHost != "" {
		return newDockerEngine(dockerConfig{
			Host:         containerHost,
			DaemonSocket: resolveDaemonSocket(containerHost, cfg.daemonSocket),
			Kind:         dockerKindPodman,
			Logger:       cfg.logger,
			Tracer:       cfg.tracer,
		})
	}

	configDir := dockerConfigDir()

	if dockerContext != "" {
		host, err := contextEndpoint(configDir, dockerContext)
		if err != nil {
			return nil, fmt.Errorf("currus: DOCKER_CONTEXT %q: %w", dockerContext, err)
		}

		return newDockerEngine(dockerConfig{
			Host:         host,
			DaemonSocket: resolveDaemonSocket(host, cfg.daemonSocket),
			Kind:         dockerKindDocker,
			Logger:       cfg.logger,
			Tracer:       cfg.tracer,
		})
	}

	if name := activeContextName(configDir); name != "" {
		host, err := contextEndpoint(configDir, name)
		if err != nil {
			return nil, fmt.Errorf("currus: active Docker context %q: %w", name, err)
		}

		return newDockerEngine(dockerConfig{
			Host:         host,
			DaemonSocket: resolveDaemonSocket(host, cfg.daemonSocket),
			Kind:         dockerKindDocker,
			Logger:       cfg.logger,
			Tracer:       cfg.tracer,
		})
	}

	return nil, nil //nolint:nilnil // intentional: no env config found, caller continues detection
}

// dockerTLSFromEnv builds a TLSConfig from DOCKER_TLS_VERIFY and
// DOCKER_CERT_PATH. Returns nil when DOCKER_TLS_VERIFY is not "1".
func dockerTLSFromEnv() (*TLSConfig, error) {
	if os.Getenv("DOCKER_TLS_VERIFY") != "1" {
		return nil, nil //nolint:nilnil // intentional: TLS not requested
	}

	certDir := os.Getenv("DOCKER_CERT_PATH")
	if certDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("determine home directory: %w", err)
		}

		certDir = filepath.Join(home, ".docker")
	}

	// G304: certDir comes from the DOCKER_CERT_PATH env var or the user's own
	// ~/.docker directory — not from untrusted network or user input.
	ca, err := os.ReadFile(filepath.Join(certDir, "ca.pem")) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("read ca.pem: %w", err)
	}

	cert, err := os.ReadFile(filepath.Join(certDir, "cert.pem")) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("read cert.pem: %w", err)
	}

	key, err := os.ReadFile(filepath.Join(certDir, "key.pem")) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("read key.pem: %w", err)
	}

	return &TLSConfig{
		CACert: ca,
		Cert:   cert,
		Key:    key,
	}, nil
}

// dockerConfigDir returns the Docker configuration directory. It reads
// DOCKER_CONFIG if set and falls back to ~/.docker.
func dockerConfigDir() string {
	if d := os.Getenv("DOCKER_CONFIG"); d != "" {
		return d
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return filepath.Join(home, ".docker")
}

// dockerConfigJSON is the subset of ~/.docker/config.json that currus reads.
// The JSON key "currentContext" is mandated by Docker's wire format, not our
// convention, so the tagliatelle snake_case rule is suppressed here.
//
//nolint:tagliatelle
type dockerConfigJSON struct {
	CurrentContext string `json:"currentContext"`
}

// activeContextName reads the active Docker context name from config.json.
// It returns "" when the file is absent, unreadable, or the context is "default".
func activeContextName(configDir string) string {
	if configDir == "" {
		return ""
	}

	// G304: configDir is derived from DOCKER_CONFIG env var or ~/.docker — not
	// from untrusted input.
	data, err := os.ReadFile(filepath.Join(configDir, "config.json")) //nolint:gosec
	if err != nil {
		return ""
	}

	var cfg dockerConfigJSON
	if err = json.Unmarshal(data, &cfg); err != nil {
		return ""
	}

	if cfg.CurrentContext == "" || cfg.CurrentContext == "default" {
		return ""
	}

	return cfg.CurrentContext
}

// dockerContextMeta is the subset of a Docker context meta.json that currus
// reads to find the Docker endpoint host.
// The JSON keys "Host" and "Endpoints" match Docker's meta.json wire format.
//
//nolint:tagliatelle
type dockerContextMeta struct {
	Endpoints map[string]struct {
		Host string `json:"Host"`
	} `json:"Endpoints"`
}

// contextEndpoint returns the Docker daemon host URI for the named Docker
// context. It reads the context metadata from the standard Docker context
// store at configDir/contexts/meta/<sha256(name)>/meta.json.
func contextEndpoint(configDir, name string) (string, error) {
	if configDir == "" {
		return "", errConfigDirUnknown
	}

	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
	metaPath := filepath.Join(configDir, "contexts", "meta", hash, "meta.json")

	// G304: metaPath is constructed from configDir (DOCKER_CONFIG or ~/.docker)
	// and the SHA-256 hash of the context name — not from untrusted input.
	data, err := os.ReadFile(metaPath) //nolint:gosec
	if err != nil {
		return "", fmt.Errorf("read context metadata: %w", err)
	}

	var meta dockerContextMeta
	if err = json.Unmarshal(data, &meta); err != nil {
		return "", fmt.Errorf("parse context metadata: %w", err)
	}

	ep, ok := meta.Endpoints["docker"]
	if !ok || ep.Host == "" {
		return "", errNoEndpointInContextMeta
	}

	return ep.Host, nil
}
