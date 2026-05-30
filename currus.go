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
	"crypto/tls"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// EngineKind is the typed selector for a specific container engine backend.
type EngineKind string

const (
	// Docker selects the Docker-API driver backed by the moby client.
	Docker EngineKind = "docker"

	// Podman selects the Docker-API driver pointed at the Podman socket.
	// Podman speaks the Docker Engine API; the same moby driver is used.
	Podman EngineKind = "podman"

	// Containerd selects the containerd v2 client driver.
	Containerd EngineKind = "containerd"
)

// ContainerID is the opaque identifier for a container returned by
// CreateContainer. Using a named type prevents accidental mix-ups with
// image refs, volume IDs, and other string identifiers.
type ContainerID string

// Container is a neutral representation of a running or stopped container.
type Container struct {
	ID     ContainerID
	Name   string
	Image  string
	State  string
	Labels map[string]string
}

// PullImageOpts controls the PullImage operation.
type PullImageOpts struct {
	Platform string
}

// StopContainerOpts controls the StopContainer operation.
type StopContainerOpts struct {
	Timeout time.Duration
}

// RemoveContainerOpts controls the RemoveContainer operation.
type RemoveContainerOpts struct {
	Force         bool
	RemoveVolumes bool
}

// ListContainersOpts filters the ListContainers result.
type ListContainersOpts struct {
	All bool
}

// Engine is the small core interface every backend implements.
//
// It carries only what every backend supports: identity, Ping, Close, and
// the universal container lifecycle (pull / create / start / stop / remove /
// list). Non-universal features live behind optional capability interfaces
// ([Logger], [Execer], [Imager], [Networker], [Volumer], [Eventer])
// discovered by type assertion:
//
//	if lg, ok := eng.(currus.Logger); ok { ... }
//
// This keeps Engine from growing into a 50-method interface and makes missing
// features a typed ok==false rather than a runtime error.
type Engine interface {
	Engine() EngineKind
	Capabilities() Caps
	Ping(ctx context.Context) error
	Close() error
	PullImage(ctx context.Context, ref string, o PullImageOpts) error
	CreateContainer(ctx context.Context, spec ContainerSpec) (ContainerID, error)
	StartContainer(ctx context.Context, id ContainerID) error
	StopContainer(ctx context.Context, id ContainerID, o StopContainerOpts) error
	RemoveContainer(ctx context.Context, id ContainerID, o RemoveContainerOpts) error
	ListContainers(ctx context.Context, o ListContainersOpts) ([]Container, error)
}

// Endpoint describes the connection details for an engine daemon.
//
// The following URI schemes are supported:
//
//   - unix:///var/run/docker.sock — local Docker socket (default)
//   - tcp://host:2376 — remote Docker over TCP (use TLSConfig for mutual TLS)
//   - ssh://user@host — remote Podman or Docker over SSH (system SSH agent)
//   - npipe:////./pipe/docker_engine — Windows named pipe
type Endpoint struct {
	// Host is the endpoint URI (see above for supported schemes).
	Host string

	// Namespace is the containerd namespace to use. Ignored by Docker and
	// Podman drivers. Empty defaults to "default".
	Namespace string

	// TLS carries the TLS configuration for tcp:// connections. Nil means
	// no client-side TLS material (the server still uses TLS if configured).
	// Ignored by unix:// and ssh:// schemes.
	TLS *TLSConfig
}

// TLSConfig carries the TLS material needed to connect to a remote engine.
type TLSConfig struct {
	CACert             []byte
	Cert               []byte
	Key                []byte
	InsecureSkipVerify bool
}

// tlsConfigFromCurrus converts a *TLSConfig into a *[tls.Config].
// Returns nil when cfg is nil.
func tlsConfigFromCurrus(cfg *TLSConfig) (*tls.Config, error) {
	if cfg == nil {
		return nil, nil //nolint:nilnil // intentional: nil means no TLS
	}
	tc := &tls.Config{
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}
	if len(cfg.Cert) > 0 && len(cfg.Key) > 0 {
		cert, err := tls.X509KeyPair(cfg.Cert, cfg.Key)
		if err != nil {
			return nil, err
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	return tc, nil
}

// Option is a functional option for New and MustNew.
type Option func(*engineConfig)

// engineConfig is the internal configuration built from Options.
type engineConfig struct {
	kind     EngineKind
	endpoint *Endpoint
	logger   *slog.Logger
	tracer   trace.TracerProvider
}

// WithEngine selects a specific engine backend instead of auto-detecting.
// Pass currus.Docker, currus.Podman, or currus.Containerd.
func WithEngine(kind EngineKind) Option {
	return func(c *engineConfig) {
		c.kind = kind
	}
}

// WithEndpoint sets the connection endpoint for the engine daemon.
// See [Endpoint] for supported URI schemes.
func WithEndpoint(ep Endpoint) Option {
	return func(c *engineConfig) {
		c.endpoint = &ep
	}
}

// WithLogger attaches a *[slog.Logger] to the engine.
func WithLogger(l *slog.Logger) Option {
	return func(c *engineConfig) {
		c.logger = l
	}
}

// WithTracerProvider attaches an OpenTelemetry TracerProvider. Each engine
// operation is wrapped in a span named "currus.<method>".
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(c *engineConfig) {
		c.tracer = tp
	}
}
