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
	"io"
	"time"
)

// Caps holds informational, non-method-shaped engine traits.
//
// Caps never mirrors a capability interface: method-shaped features
// (e.g. ContainerLogs) are discovered by type assertion against the
// capability interfaces (Logger, Execer, …). Caps holds only boolean or
// string descriptors of structural engine properties.
type Caps struct {
	RootlessCapable bool
	SupportsPods    bool
	OneShotRun      bool
	NamespaceModel  string
}

// Logger is the capability interface for reading container log streams.
//
// This is a capability — not part of the core Engine — because containerd
// has no native container logs. Callers must check the assertion:
//
//	if lg, ok := eng.(currus.Logger); ok {
//	    rc, _ := lg.ContainerLogs(ctx, id, currus.ContainerLogsOpts{})
//	    defer rc.Close()
//	    io.Copy(os.Stdout, rc)
//	}
//
// The returned [io.ReadCloser] always produces plain, demultiplexed output.
// Implementations are responsible for stripping any transport framing
// (e.g. the Docker 8-byte stream headers) before returning the reader.
type Logger interface {
	ContainerLogs(ctx context.Context, id ContainerID, o ContainerLogsOpts) (io.ReadCloser, error)
}

// ContainerLogsOpts controls what the ContainerLogs stream returns.
type ContainerLogsOpts struct {
	Follow bool
	Tail   int
}

// Execer is the capability interface for running commands inside a container.
type Execer interface {
	Exec(ctx context.Context, id ContainerID, o ExecOpts) (ExecResult, error)
}

// ExecOpts configures an Exec call.
type ExecOpts struct {
	Cmd          []string
	Env          []string
	WorkingDir   string
	AttachStdout bool
	AttachStderr bool
}

// ExecResult holds the outcome of an Exec call.
type ExecResult struct {
	ExitCode int
	Stdout   io.Reader
	Stderr   io.Reader
}

// Imager is the capability interface for image management beyond PullImage.
type Imager interface {
	ListImages(ctx context.Context, o ListImagesOpts) ([]Image, error)
	RemoveImage(ctx context.Context, ref string, o RemoveImageOpts) error
	TagImage(ctx context.Context, src, dst string) error
}

// ListImagesOpts filters the ListImages result.
type ListImagesOpts struct {
	All bool
}

// RemoveImageOpts controls the RemoveImage behavior.
type RemoveImageOpts struct {
	Force bool
}

// Image is a neutral representation of an image present in the engine.
type Image struct {
	ID        string
	Tags      []string
	SizeBytes int64
}

// Networker is the capability interface for managing container networks.
// Implemented by Docker and Podman; not available on containerd.
type Networker interface {
	CreateNetwork(ctx context.Context, name string, o CreateNetworkOpts) (NetworkID, error)
	ListNetworks(ctx context.Context, o ListNetworksOpts) ([]Network, error)
	RemoveNetwork(ctx context.Context, id NetworkID, o RemoveNetworkOpts) error
	// ConnectContainer connects a running container to a network.
	ConnectContainer(ctx context.Context, net NetworkID, id ContainerID, o ConnectOpts) error
	// DisconnectContainer disconnects a container from a network.
	DisconnectContainer(ctx context.Context, net NetworkID, id ContainerID, o DisconnectOpts) error
}

// ConnectOpts controls ConnectContainer.
type ConnectOpts struct {
	// Aliases are optional extra DNS names for this container on the network.
	Aliases []string
}

// DisconnectOpts controls DisconnectContainer.
type DisconnectOpts struct {
	// Force disconnects the container even if it is running.
	Force bool
}

// NetworkID is the opaque identifier for a container network.
type NetworkID string

// CreateNetworkOpts controls CreateNetwork.
type CreateNetworkOpts struct {
	Driver string
}

// ListNetworksOpts filters the ListNetworks result.
type ListNetworksOpts struct{}

// RemoveNetworkOpts controls RemoveNetwork.
type RemoveNetworkOpts struct {
	Force bool
}

// Network is a neutral representation of a container network.
type Network struct {
	ID     NetworkID
	Name   string
	Driver string
}

// Volumer is the capability interface for managing named volumes.
// Implemented by Docker and Podman; not available on containerd.
type Volumer interface {
	CreateVolume(ctx context.Context, name string, o CreateVolumeOpts) (VolumeID, error)
	ListVolumes(ctx context.Context, o ListVolumesOpts) ([]Volume, error)
	RemoveVolume(ctx context.Context, id VolumeID, o RemoveVolumeOpts) error
}

// VolumeID is the opaque identifier for a named volume.
type VolumeID string

// CreateVolumeOpts controls CreateVolume.
type CreateVolumeOpts struct {
	Driver string
}

// ListVolumesOpts filters the ListVolumes result.
type ListVolumesOpts struct{}

// RemoveVolumeOpts controls RemoveVolume.
type RemoveVolumeOpts struct {
	Force bool
}

// Volume is a neutral representation of a named volume.
type Volume struct {
	ID         VolumeID
	Driver     string
	Mountpoint string
}

// Copier is the capability interface for copying files into and out of a
// container.
// Implemented by Docker and Podman; not available on containerd.
type Copier interface {
	// CopyToContainer copies a TAR-archived content stream into the container
	// at DestPath.
	CopyToContainer(ctx context.Context, id ContainerID, o CopyToContainerOpts) error

	// CopyFromContainer copies a path from the container's filesystem and
	// returns a TAR-archived stream of the content.
	CopyFromContainer(ctx context.Context, id ContainerID, o CopyFromContainerOpts) (io.ReadCloser, error)
}

// CopyToContainerOpts controls CopyToContainer.
type CopyToContainerOpts struct {
	// DestPath is the path inside the container to copy the content to.
	DestPath string
	// Content is a TAR archive reader to copy into the container.
	Content io.Reader
}

// CopyFromContainerOpts controls CopyFromContainer.
type CopyFromContainerOpts struct {
	// SrcPath is the path inside the container to copy content from.
	SrcPath string
}

// Inspector is the capability interface for reading full container metadata.
// Implemented by Docker and Podman; not available on containerd.
//
// Usage:
//
//	if ins, ok := eng.(currus.Inspector); ok {
//	    info, _ := ins.Inspect(ctx, id)
//	}
type Inspector interface {
	Inspect(ctx context.Context, id ContainerID) (ContainerInfo, error)
}

// ContainerInfo holds the full details of a container as returned by Inspect.
type ContainerInfo struct {
	ID         ContainerID
	Name       string
	Image      string
	ImageID    string
	Labels     map[string]string
	State      ContainerState
	Command    []string
	Env        []string
	WorkingDir string
	Mounts     []Mount
}

// ContainerState holds the runtime state of an inspected container.
type ContainerState struct {
	Running    bool
	Paused     bool
	Restarting bool
	ExitCode   int
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
}

// Stater is the capability interface for reading point-in-time resource usage.
// Implemented by Docker and Podman; not available on containerd.
type Stater interface {
	Stats(ctx context.Context, id ContainerID, o StatsOpts) (ContainerStats, error)
}

// StatsOpts controls the Stats call (reserved for future extension).
type StatsOpts struct{}

// ContainerStats holds point-in-time resource usage statistics for a container.
type ContainerStats struct {
	CPUPercent    float64
	MemoryUsage   uint64
	MemoryLimit   uint64
	NetworkInput  uint64
	NetworkOutput uint64
}

// Waiter is the capability interface for blocking until a container exits.
// Implemented by Docker and Podman; not available on containerd.
type Waiter interface {
	WaitContainer(ctx context.Context, id ContainerID, o WaitContainerOpts) (<-chan WaitResult, error)
}

// WaitCondition describes the event to wait for in a WaitContainer call.
type WaitCondition string

const (
	// WaitConditionNotRunning waits until the container is no longer running.
	// This is the default when Condition is empty.
	WaitConditionNotRunning WaitCondition = "not-running"

	// WaitConditionNextExit waits for the container's next exit event.
	WaitConditionNextExit WaitCondition = "next-exit"

	// WaitConditionRemoved waits until the container has been removed.
	WaitConditionRemoved WaitCondition = "removed"
)

// WaitContainerOpts controls WaitContainer.
type WaitContainerOpts struct {
	// Condition is the event to wait for. Empty defaults to WaitConditionNotRunning.
	Condition WaitCondition
}

// WaitResult is the outcome of a WaitContainer call.
type WaitResult struct {
	StatusCode int
	Error      string
}

// Eventer is the capability interface for subscribing to engine events.
// Implemented by Docker and Podman; not available on containerd.
type Eventer interface {
	Events(ctx context.Context) (<-chan Event, error)
}

// Event is a neutral representation of an engine lifecycle event.
type Event struct {
	Type   string
	Action string
	Actor  string
}

// EndpointReporter reports the endpoint the engine actually connected to.
//
// Use [Endpoint.DaemonSocket] when bind-mounting the engine socket into a
// sidecar container. On VM-based setups (Lima, Colima, Docker Desktop,
// OrbStack) the daemon socket path inside the VM differs from the forwarded
// socket path on the host; DaemonSocket always reflects the correct in-VM
// path:
//
//	if er, ok := eng.(currus.EndpointReporter); ok {
//	    sock := er.Endpoint().DaemonSocket // use this for bind mounts
//	}
//
// DaemonSocket is empty for non-unix endpoints (tcp://, ssh://) where
// bind-mounting is not possible.
//
// Implemented by Docker, Podman, and containerd drivers.
type EndpointReporter interface {
	Endpoint() Endpoint
}
