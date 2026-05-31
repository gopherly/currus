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
	"fmt"
	"log/slog"
	"os"
)

// New creates an Engine by detecting or constructing the appropriate backend.
//
// With no options, New probes endpoints in priority order until a reachable
// engine responds to Ping:
//
//  1. CONTAINER_ENGINE env var override
//  2. Docker socket (unix:///var/run/docker.sock)
//  3. Podman rootless socket (~/.local/share/containers/podman/machine/
//     podman.sock or $XDG_RUNTIME_DIR/podman/podman.sock)
//  4. Podman rootful socket (unix:///run/podman/podman.sock)
//  5. containerd socket (unix:///run/containerd/containerd.sock)
//
// Use [WithEngine] to skip detection and select a backend explicitly.
// Use [WithEndpoint] to override the default socket path.
func New(ctx context.Context, opts ...Option) (Engine, error) {
	cfg := buildEngineConfig(opts)

	if cfg.kind != "" {
		return openKind(cfg.kind, cfg)
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
			Host:   host,
			Kind:   dockerKindDocker,
			TLS:    tlsCfg,
			Logger: cfg.logger,
			Tracer: cfg.tracer,
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
			Host:   host,
			Kind:   dockerKindPodman,
			TLS:    tlsCfg,
			Logger: cfg.logger,
			Tracer: cfg.tracer,
		})
	case Containerd:
		socket := ""
		ns := ""
		if cfg.endpoint != nil {
			socket = cfg.endpoint.Host
			ns = cfg.endpoint.Namespace
		}

		return newContainerdEngine(containerdConfig{
			Socket:    socket,
			Namespace: ns,
			Logger:    cfg.logger,
			Tracer:    cfg.tracer,
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
		return candidate{
			kind:   kind,
			socket: socket,
			open: func() (Engine, error) {
				return newDockerEngine(dockerConfig{
					Host:   "unix://" + socket,
					Kind:   dkind,
					Logger: cfg.logger,
					Tracer: cfg.tracer,
				})
			},
		}
	}

	ctrdSocket := defaultContainerdSocket
	candidates := []candidate{
		dockerCandidate(defaultDockerSocket, dockerKindDocker, Docker),
		dockerCandidate(podmanRootlessSocket(), dockerKindPodman, Podman),
		dockerCandidate(defaultPodmanRootfulSocket, dockerKindPodman, Podman),
		{
			kind:   Containerd,
			socket: ctrdSocket,
			open: func() (Engine, error) {
				return newContainerdEngine(containerdConfig{
					Socket: ctrdSocket,
					Logger: cfg.logger,
					Tracer: cfg.tracer,
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
