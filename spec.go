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

// ContainerSpec is the desired state of a container.
//
// It is built from the common core of OCI runtime spec, Docker container
// config, and Podman specgen. Growth is additive within the nested sub-structs
// so callers that set only a few fields are unaffected by new additions.
type ContainerSpec struct {
	// Image is the container image reference (e.g. "docker.io/library/alpine:3.20").
	Image string
	// Name is an optional human-readable name assigned to the container.
	Name string
	// Command overrides the image ENTRYPOINT. Leave nil to use the image default.
	Command []string
	// Args are the arguments passed to Command, or to the image ENTRYPOINT when
	// Command is nil.
	Args []string
	// Env holds environment variables in KEY=VALUE form.
	Env []string
	// WorkingDir sets the working directory for the container process. Leave
	// empty to use the image default.
	WorkingDir string
	// Labels are arbitrary key/value metadata attached to the container.
	Labels map[string]string
	// Mounts lists filesystem mounts attached to the container.
	Mounts []Mount
	// Ports lists port bindings between the container and the host.
	Ports []Port
	// Resources constrains the CPU and memory available to the container.
	Resources Resources
	// Restart is the restart policy applied when the container exits.
	Restart RestartPolicy
	// Networks lists the networks to join at creation time, in order.
	Networks []NetworkAttachment
}

// NetworkAttachment describes a single network a container should join.
type NetworkAttachment struct {
	// Name is the network name or ID to join (e.g. "kind").
	Name string
	// Aliases are optional extra DNS names for this container on the network.
	Aliases []string
}

// MountType identifies the kind of filesystem mount.
type MountType string

const (
	// MountTypeBind is a bind mount from a host path.
	MountTypeBind MountType = "bind"
	// MountTypeVolume is a named engine-managed volume.
	MountTypeVolume MountType = "volume"
	// MountTypeTmpfs is an in-memory tmpfs mount.
	MountTypeTmpfs MountType = "tmpfs"
)

// Mount describes a single filesystem mount attached to a container.
type Mount struct {
	// Type identifies the kind of mount (bind, volume, or tmpfs).
	Type MountType
	// Source is the host path for bind mounts or the volume name for named volumes.
	Source string
	// Target is the absolute path inside the container where the mount is attached.
	Target string
	// ReadOnly makes the mount read-only when true.
	ReadOnly bool
}

// Port describes a single port binding between container and host.
type Port struct {
	// Container is the port number exposed by the container.
	Container uint16
	// Host is the host port to bind to. Zero lets the engine pick a free port.
	Host uint16
	// Protocol is the transport protocol ("tcp" or "udp"). Defaults to "tcp"
	// when empty.
	Protocol string
}

// Resources constrains the CPU and memory available to a container.
type Resources struct {
	// NanoCPUs is the CPU limit expressed in units of 1e-9 CPUs (e.g. 500_000_000
	// for half a CPU). Zero means no limit.
	NanoCPUs int64
	// MemoryBytes is the memory limit in bytes. Zero means no limit.
	MemoryBytes int64
}

// RestartPolicy describes when and how many times the engine should restart
// a container after it exits.
type RestartPolicy struct {
	// Mode is the restart mode. Accepted values depend on the engine (e.g.
	// "always", "on-failure", "unless-stopped"). Empty means no restart.
	Mode string
	// MaxRetries is the maximum number of restart attempts. Only meaningful
	// with the "on-failure" mode; ignored otherwise.
	MaxRetries int
}
