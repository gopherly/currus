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

// The containerd model differs from Docker in two fundamental ways:
//
//  1. Container lifecycle is split: NewContainer creates the container spec,
//     NewTask starts the process (the "task"), and they are deleted separately.
//
//  2. There are no native container logs accessible through the client API.
//     The Logger capability is therefore NOT implemented by this driver;
//     callers must branch on the capability assertion.
//
// This driver adapts containerd to Currus's Docker-like container model. The
// Container ID returned from CreateContainer is the containerd container ID.
// The snapshot ID uses the same string with a "-snapshot" suffix. Container
// state ("running", "exited", "created") is derived from the task status when
// available.

package currus

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"syscall"
	"time"

	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"go.opentelemetry.io/otel/trace"

	containerd "github.com/containerd/containerd/v2/client"
	cerrdefs "github.com/containerd/errdefs"
)

// defaultContainerdSocket is the well-known containerd socket path.
const defaultContainerdSocket = "/run/containerd/containerd.sock"

// defaultContainerdNamespace is the containerd namespace used when none is
// configured.
const defaultContainerdNamespace = "default"

// containerdConfig holds the parameters needed to build a containerdEngine.
type containerdConfig struct {
	// Socket is the path to the containerd socket. Empty uses the default.
	Socket string

	// Namespace is the containerd namespace to operate in. Empty uses
	// defaultContainerdNamespace.
	Namespace string

	// Logger is the slog logger. Nil is replaced with slog.Default().
	Logger *slog.Logger

	// Tracer is the OTel TracerProvider. Nil means no tracing.
	Tracer trace.TracerProvider
}

// containerdEngine is the containerd v2 driver.
// It implements Engine. It does NOT implement Logger because
// containerd has no native container logs API.
type containerdEngine struct {
	cli       *containerd.Client
	namespace string
	socket    string // resolved socket path, e.g. "/run/containerd/containerd.sock"
	logger    *slog.Logger
	tracer    trace.TracerProvider
}

// Compile-time assertions.
var (
	_ Engine           = (*containerdEngine)(nil)
	_ EndpointReporter = (*containerdEngine)(nil)
)

// newContainerdEngine creates a containerdEngine using the given containerdConfig.
func newContainerdEngine(cfg containerdConfig) (*containerdEngine, error) {
	// Accept both a raw filesystem path and a unix:// URI; containerd.New and
	// the dialer both expect the raw path.
	socket := strings.TrimPrefix(cfg.Socket, "unix://")
	if socket == "" {
		socket = defaultContainerdSocket
	}

	ns := cfg.Namespace
	if ns == "" {
		ns = defaultContainerdNamespace
	}

	lg := cfg.Logger
	if lg == nil {
		lg = slog.Default()
	}

	cli, err := containerd.New(socket)
	if err != nil {
		return nil, fmt.Errorf("containerd: open socket %s: %w", socket, err)
	}

	return &containerdEngine{
		cli:       cli,
		namespace: ns,
		socket:    socket,
		logger:    lg,
		tracer:    cfg.Tracer,
	}, nil
}

// ctrdCtx returns a context decorated with the configured containerd namespace.
func (e *containerdEngine) ctrdCtx(ctx context.Context) context.Context {
	return namespaces.WithNamespace(ctx, e.namespace)
}

// Kind returns the backend kind.
func (e *containerdEngine) Kind() EngineKind {
	return Containerd
}

// Capabilities returns the non-method-shaped traits of this driver.
// OneShotRun is false (containerd uses create+task+start); NamespaceModel is
// "containerd".
func (e *containerdEngine) Capabilities() Caps {
	return Caps{
		NamespaceModel: "containerd",
	}
}

// Ping verifies the containerd daemon is reachable.
func (e *containerdEngine) Ping(ctx context.Context) error {
	_, err := e.cli.Version(e.ctrdCtx(ctx))
	if err != nil {
		return fmt.Errorf("containerd: ping: %w", err)
	}

	return nil
}

// Close closes the containerd client connection.
func (e *containerdEngine) Close() error {
	return e.cli.Close()
}

// PullImage fetches the image from a registry and unpacks it into the snapshot
// store. WithPullUnpack prepares snapshots for immediate container creation.
func (e *containerdEngine) PullImage(ctx context.Context, ref string, o PullImageOpts) error {
	e.logger.DebugContext(ctx, "pull image", "ref", ref)
	pullOpts := []containerd.RemoteOpt{containerd.WithPullUnpack}
	if o.Platform != "" {
		pullOpts = append(pullOpts, containerd.WithPlatform(o.Platform))
	}
	_, err := e.cli.Pull(e.ctrdCtx(ctx), ref, pullOpts...)
	if err != nil {
		return mapCtrdErr(fmt.Errorf("containerd: pull %s: %w", ref, err))
	}

	return nil
}

// CreateContainer creates a container spec and allocates a snapshot for its
// root filesystem. It does NOT start the container process (use StartContainer).
func (e *containerdEngine) CreateContainer(ctx context.Context, spec ContainerSpec) (ContainerID, error) {
	e.logger.DebugContext(ctx, "create container", "image", spec.Image, "name", spec.Name)

	if err := spec.Validate(); err != nil {
		return "", err
	}

	nctx := e.ctrdCtx(ctx)

	img, err := e.cli.GetImage(nctx, spec.Image)
	if err != nil {
		return "", mapCtrdErr(fmt.Errorf("containerd: get image %s: %w", spec.Image, err))
	}

	id := spec.Name
	if id == "" {
		id = fmt.Sprintf("currus-%d", time.Now().UnixNano())
	}
	snapshotID := id + "-snapshot"

	specOpts := []oci.SpecOpts{
		oci.WithImageConfig(img),
	}
	if len(spec.Command) > 0 || len(spec.Args) > 0 {
		args := make([]string, 0, len(spec.Command)+len(spec.Args))
		args = append(args, spec.Command...)
		args = append(args, spec.Args...)
		specOpts = append(specOpts, oci.WithProcessArgs(args...))
	}
	if len(spec.Env) > 0 {
		specOpts = append(specOpts, oci.WithEnv(spec.Env))
	}

	c, err := e.cli.NewContainer(nctx, id,
		containerd.WithImage(img),
		containerd.WithNewSnapshot(snapshotID, img),
		containerd.WithNewSpec(specOpts...),
	)
	if err != nil {
		return "", mapCtrdErr(fmt.Errorf("containerd: new container %s: %w", id, err))
	}

	e.logger.DebugContext(ctx, "container created", "id", c.ID())

	return ContainerID(c.ID()), nil
}

// StartContainer loads the container, creates a task with NullIO, and starts it.
func (e *containerdEngine) StartContainer(ctx context.Context, id ContainerID) error {
	e.logger.DebugContext(ctx, "start container", "id", id)
	nctx := e.ctrdCtx(ctx)

	c, err := e.cli.LoadContainer(nctx, string(id))
	if err != nil {
		return mapCtrdErr(fmt.Errorf("containerd: load container %s: %w", id, err))
	}

	task, err := c.NewTask(nctx, cio.NullIO)
	if err != nil {
		return mapCtrdErr(fmt.Errorf("containerd: new task %s: %w", id, err))
	}

	if err = task.Start(nctx); err != nil {
		_, _ = task.Delete(nctx) //nolint:errcheck // best-effort cleanup
		return mapCtrdErr(fmt.Errorf("containerd: start task %s: %w", id, err))
	}

	return nil
}

// StopContainer sends SIGTERM to the container's task and waits for it to exit,
// falling back to SIGKILL after the timeout.
func (e *containerdEngine) StopContainer(ctx context.Context, id ContainerID, o StopContainerOpts) error {
	e.logger.DebugContext(ctx, "stop container", "id", id)
	nctx := e.ctrdCtx(ctx)

	c, err := e.cli.LoadContainer(nctx, string(id))
	if err != nil {
		return mapCtrdErr(fmt.Errorf("containerd: load container %s: %w", id, err))
	}

	task, err := c.Task(nctx, nil)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return fmt.Errorf("containerd: stop %s: %w", id, ErrNotFound)
		}

		return fmt.Errorf("containerd: get task %s: %w", id, err)
	}

	exitCh, err := task.Wait(nctx)
	if err != nil {
		return fmt.Errorf("containerd: wait %s: %w", id, err)
	}

	if err = task.Kill(nctx, syscall.SIGTERM); err != nil {
		return fmt.Errorf("containerd: kill %s: %w", id, err)
	}

	timeout := o.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	select {
	case <-exitCh:
		_, _ = task.Delete(nctx) //nolint:errcheck // best-effort cleanup
		return nil
	case <-ctx.Done():
		return fmt.Errorf("containerd: stop %s: %w", id, ctx.Err())
	case <-time.After(timeout):
		_ = task.Kill(nctx, syscall.SIGKILL) //nolint:errcheck // best-effort
		select {
		case <-exitCh:
		case <-ctx.Done():
			return fmt.Errorf("containerd: stop %s (after SIGKILL): %w", id, ctx.Err())
		}
		_, _ = task.Delete(nctx) //nolint:errcheck // best-effort cleanup

		return nil
	}
}

// RemoveContainer deletes the container and its snapshot.
func (e *containerdEngine) RemoveContainer(ctx context.Context, id ContainerID, o RemoveContainerOpts) error {
	e.logger.DebugContext(ctx, "remove container", "id", id)
	nctx := e.ctrdCtx(ctx)

	c, err := e.cli.LoadContainer(nctx, string(id))
	if err != nil {
		return mapCtrdErr(fmt.Errorf("containerd: load container %s: %w", id, err))
	}

	if o.Force {
		// Best-effort: kill the task if running.
		if task, terr := c.Task(nctx, nil); terr == nil {
			_ = task.Kill(nctx, syscall.SIGKILL) //nolint:errcheck // best-effort
			exitCh, _ := task.Wait(nctx)         //nolint:errcheck // best-effort
			if exitCh != nil {
				select {
				case <-exitCh:
				case <-time.After(5 * time.Second):
				}
			}
			_, _ = task.Delete(nctx) //nolint:errcheck // best-effort cleanup
		}
	}

	if err = c.Delete(nctx, containerd.WithSnapshotCleanup); err != nil {
		return mapCtrdErr(fmt.Errorf("containerd: delete container %s: %w", id, err))
	}

	return nil
}

// ListContainers returns all containers in the configured namespace.
// The state is derived from the task status where available.
func (e *containerdEngine) ListContainers(ctx context.Context, o ListContainersOpts) ([]Container, error) {
	nctx := e.ctrdCtx(ctx)

	containers, err := e.cli.Containers(nctx)
	if err != nil {
		return nil, mapCtrdErr(fmt.Errorf("containerd: list containers: %w", err))
	}

	out := make([]Container, 0, len(containers))
	for _, c := range containers {
		state := ctrdContainerState(nctx, c)
		if !o.All && state != "running" {
			continue
		}
		labels, _ := c.Labels(nctx) //nolint:errcheck // best-effort
		imgName := ""
		info, infoErr := c.Info(nctx)
		if infoErr == nil {
			imgName = info.Image
		}
		out = append(out, Container{
			ID:     ContainerID(c.ID()),
			Name:   c.ID(),
			Image:  imgName,
			State:  state,
			Labels: labels,
		})
	}

	return out, nil
}

// ctrdContainerState returns a Docker-like state string for the container by
// inspecting its task. Returns "running", "exited", or "created".
func ctrdContainerState(ctx context.Context, c containerd.Container) string {
	task, err := c.Task(ctx, nil)
	if err != nil {
		return "created"
	}
	status, err := task.Status(ctx)
	if err != nil {
		return "created"
	}
	switch status.Status {
	case containerd.Running:
		return "running"
	case containerd.Stopped:
		return "exited"
	case containerd.Paused, containerd.Pausing:
		return "paused"
	default:
		return strings.ToLower(string(status.Status))
	}
}

// Endpoint implements the EndpointReporter capability.
// Networks are not supported by the containerd driver; the ContainerSpec.Networks
// field is silently ignored.
func (e *containerdEngine) Endpoint() Endpoint {
	return Endpoint{
		Host:      "unix://" + e.socket,
		Namespace: e.namespace,
	}
}

// mapCtrdErr translates containerd error types into the sentinel taxonomy.
func mapCtrdErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case cerrdefs.IsNotFound(err):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case cerrdefs.IsAlreadyExists(err):
		return fmt.Errorf("%w: %w", ErrAlreadyExists, err)
	case cerrdefs.IsConflict(err):
		return fmt.Errorf("%w: %w", ErrConflict, err)
	default:
		return err
	}
}
