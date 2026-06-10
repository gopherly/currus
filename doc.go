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

// Package currus provides a single, neutral API for the container lifecycle
// that auto-detects and drives Docker, Podman, or containerd through engine
// client APIs, never by shelling out to a CLI.
//
// # Architecture
//
// Currus follows the database/sql shape: a neutral interface on top, with
// pluggable engine drivers beneath. Engine selection is auto-detected by
// default (probing endpoints in priority order with a Ping validation), or
// explicit via [WithEngine].
//
// The neutral container model is explicitly Docker-like. Engines that do not
// natively implement it (notably containerd) are adapted to it. Where an
// engine cannot express part of the model, that part is surfaced via a
// capability interface and [Caps] rather than silently faked.
//
// # Quick start
//
// Zero-config: detect whatever engine is installed.
//
//	eng := currus.MustNew(ctx, currus.WithLogger(slog.Default()))
//	defer eng.Close()
//
//	if err := eng.PullImage(ctx, "docker.io/library/redis:7", currus.PullImageOpts{}); err != nil {
//	    log.Fatal(err)
//	}
//
//	id, err := eng.CreateContainer(ctx, currus.ContainerSpec{
//	    Image: "docker.io/library/redis:7",
//	    Name:  "cache",
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	if err := eng.StartContainer(ctx, id); err != nil {
//	    log.Fatal(err)
//	}
//
// Explicit engine selection:
//
//	eng, err := currus.New(ctx, currus.WithEngine(currus.Podman))
//
// Remote Docker over TLS:
//
//	eng, err := currus.New(ctx,
//	    currus.WithEngine(currus.Docker),
//	    currus.WithEndpoint(currus.Endpoint{
//	        Host: "tcp://docker-host:2376",
//	        TLS: &currus.TLSConfig{
//	            CACert: caCertPEM,
//	            Cert:   certPEM,
//	            Key:    keyPEM,
//	        },
//	    }),
//	)
//
// # Engine detection order
//
// When no [WithEngine] option is given, [New] resolves the engine in this
// order and returns the first one that responds to [Engine.Ping]:
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
// DOCKER_HOST and DOCKER_CONTEXT are mutually exclusive. Setting both returns
// an error wrapping [ErrInvalidSpec].
//
// # Engine interface
//
// [Engine] exposes the small core that every backend supports: identity,
// Ping, Close, and the universal container lifecycle (pull/create/start/
// stop/remove/list). Non-universal features live behind optional capability
// interfaces discovered by type assertion:
//
//	if lg, ok := eng.(currus.Logger); ok {
//	    rc, _ := lg.ContainerLogs(ctx, id, currus.ContainerLogsOpts{})
//	    defer rc.Close()
//	    io.Copy(os.Stdout, rc)
//	}
//
// # Network attachment
//
// Containers can join one or more networks at creation time by populating
// [ContainerSpec.Networks]. The first network is attached before the
// container starts; additional networks are connected immediately after
// create. Both Docker and Podman honor this field; containerd ignores it.
//
//	id, err := eng.CreateContainer(ctx, currus.ContainerSpec{
//	    Image:    "ghcr.io/example/sidecar:latest",
//	    Name:     "kind-sidecar",
//	    Networks: []currus.NetworkAttachment{{Name: "kind"}},
//	})
//
// The [Networker] capability also exposes [Networker.ConnectContainer] and
// [Networker.DisconnectContainer] for attaching and detaching after start.
//
// # Security, DNS, and init
//
// [ContainerSpec.Security] controls the container's security posture:
// process identity ([Security.User]), supplementary groups, privileged mode,
// and Linux capability adjustments ([Security.AddCapabilities] /
// [Security.DropCapabilities]). A common hardening pattern is:
//
//	id, err := eng.CreateContainer(ctx, currus.ContainerSpec{
//	    Image: "ghcr.io/example/app:latest",
//	    Security: currus.Security{
//	        User:             "1000:1000",
//	        DropCapabilities: []currus.Capability{currus.CapAll},
//	        AddCapabilities:  []currus.Capability{currus.CapNetBindService},
//	    },
//	})
//
// Docker and Podman honor all Security fields. The containerd driver maps
// User, Privileged, AddCapabilities, and DropCapabilities via OCI spec opts;
// Groups and SecurityOpts are silently ignored.
//
// [ContainerSpec.DNS], [ContainerSpec.Hostname], [ContainerSpec.ExtraHosts],
// and [ContainerSpec.Init] are honored by Docker and Podman; containerd
// ignores them.
//
// # Resolved endpoint
//
// The [EndpointReporter] capability exposes the URI the engine actually
// connected to. Use [Endpoint.DaemonSocket] when bind-mounting the daemon
// socket into a sidecar container. On VM-based setups (Lima, Colima, Docker
// Desktop, OrbStack) the daemon socket path inside the VM differs from the
// forwarded socket on the host; DaemonSocket always holds the correct in-VM
// path:
//
//	if er, ok := eng.(currus.EndpointReporter); ok {
//	    ep := er.Endpoint()
//	    sock := ep.DaemonSocket // use for bind mounts; empty for tcp/ssh
//	}
//
// # Capability matrix
//
// The following table shows which capabilities each engine implements.
// A "—" means the engine does not implement the interface; type-asserting
// it yields ok == false.
//
//	Capability       │ Docker │ Podman │ containerd
//	─────────────────┼────────┼────────┼───────────
//	Engine           │ ✓      │ ✓      │ ✓
//	Logger           │ ✓      │ ✓      │ —
//	Execer           │ ✓      │ ✓      │ —
//	Inspector        │ ✓      │ ✓      │ —
//	Stater           │ ✓      │ ✓      │ —
//	Waiter           │ ✓      │ ✓      │ —
//	Eventer          │ ✓      │ ✓      │ —
//	Imager           │ ✓      │ ✓      │ —
//	Networker        │ ✓      │ ✓      │ —
//	Volumer          │ ✓      │ ✓      │ —
//	Copier           │ ✓      │ ✓      │ —
//	EndpointReporter │ ✓      │ ✓      │ ✓
//
// # ContainerSpec field support
//
// The following table shows which [ContainerSpec] fields each engine honors.
// A "—" means the field is silently ignored by that engine.
//
//	Field                      │ Docker │ Podman │ containerd
//	───────────────────────────┼────────┼────────┼───────────
//	Security.User              │ ✓      │ ✓      │ ✓
//	Security.Privileged        │ ✓      │ ✓      │ ✓
//	Security.AddCapabilities   │ ✓      │ ✓      │ ✓
//	Security.DropCapabilities  │ ✓      │ ✓      │ ✓
//	Security.Groups            │ ✓      │ ✓      │ —
//	Security.SecurityOpts      │ ✓      │ ✓      │ —
//	DNS                        │ ✓      │ ✓      │ —
//	Hostname                   │ ✓      │ ✓      │ —
//	ExtraHosts                 │ ✓      │ ✓      │ —
//	Init                       │ ✓      │ ✓      │ —
//
// # Error handling
//
// Errors are normalized into a stable sentinel taxonomy ([ErrNotFound],
// [ErrAlreadyExists], [ErrConflict], [ErrNotImplemented], [ErrUnsupported])
// usable with [errors.Is] / [errors.As] across every engine:
//
//	if errors.Is(err, currus.ErrNotFound) { ... }
//
// # Testing
//
// Swap the real engine for the in-memory fake from
// [gopherly.dev/currus/currustest]:
//
//	eng := currustest.New() // implements Engine + all capability interfaces
//
// The [gopherly.dev/currus/conformance] package provides a shared behavioral
// test suite that verifies any Engine implementation. Driver maintainers can
// run it against both the in-memory fake and real daemons.
//
// # Platform support
//
// The Docker-API driver (serving Docker and Podman) is pure HTTP and builds
// on Linux, macOS, and Windows. The containerd driver is supported on Linux
// only (containerd is not available on macOS or Windows outside of a VM).
// Rootless Docker and rootless Podman are fully supported via auto-detection
// through the XDG_RUNTIME_DIR socket paths.
//
// For more details, see https://pkg.go.dev/gopherly.dev/currus
package currus
