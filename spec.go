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

import "fmt"

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
	// Security configures the container's security posture (user, capabilities,
	// privileged mode). Zero value means engine defaults.
	// Docker and Podman honor all fields. containerd maps User, Privileged,
	// AddCapabilities, and DropCapabilities; other fields are ignored.
	Security Security
	// DNS configures container DNS resolution.
	// Honored by Docker and Podman; ignored by containerd.
	DNS DNS
	// Hostname sets the container's hostname. Empty uses the engine default.
	// Honored by Docker and Podman; ignored by containerd.
	Hostname string
	// ExtraHosts adds custom host-to-IP mappings in "host:ip" format
	// (e.g. "db:10.0.0.1"). Honored by Docker and Podman; ignored by containerd.
	ExtraHosts []string
	// Init enables an init process (PID 1) for signal forwarding and zombie
	// reaping. Honored by Docker and Podman; ignored by containerd.
	Init bool
}

// Validate reports whether the spec is well-formed, returning a wrapped
// [ErrInvalidSpec] when a required field is missing. Drivers call it before
// create; callers may also use it to check a spec ahead of time.
func (s ContainerSpec) Validate() error {
	if s.Image == "" {
		return fmt.Errorf("%w: image is required", ErrInvalidSpec)
	}

	return nil
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

// RestartMode describes the restart strategy for a container.
type RestartMode string

const (
	// RestartNever disables automatic restarts. This is the default.
	RestartNever RestartMode = ""

	// RestartAlways restarts the container regardless of exit status.
	RestartAlways RestartMode = "always"

	// RestartOnFailure restarts the container only when it exits with a
	// non-zero exit code. Use MaxRetries to cap the number of attempts.
	RestartOnFailure RestartMode = "on-failure"

	// RestartUnlessStopped restarts the container unless it was explicitly
	// stopped by the user.
	RestartUnlessStopped RestartMode = "unless-stopped"
)

// RestartPolicy describes when and how many times the engine should restart
// a container after it exits.
type RestartPolicy struct {
	// Mode is the restart strategy. Empty (RestartNever) means no restart.
	Mode RestartMode
	// MaxRetries is the maximum number of restart attempts. Only meaningful
	// with RestartOnFailure; ignored otherwise.
	MaxRetries int
}

// Capability is a Linux capability name (e.g. "NET_ADMIN", "SYS_PTRACE").
// Use the Cap* constants for discoverability; any valid capability string can
// be cast directly: Capability("CUSTOM_CAP").
type Capability string

const (
	// CapAll represents all capabilities. Commonly used with DropCapabilities
	// for a drop-all-then-add pattern.
	CapAll Capability = "ALL"
	// CapAuditWrite allows writing to the kernel audit log.
	CapAuditWrite Capability = "AUDIT_WRITE"
	// CapChown allows arbitrary changes to file UIDs and GIDs.
	CapChown Capability = "CHOWN"
	// CapDacOverride bypasses read, write, and execute permission checks.
	CapDacOverride Capability = "DAC_OVERRIDE"
	// CapDacReadSearch bypasses read permission checks and directory execute checks.
	CapDacReadSearch Capability = "DAC_READ_SEARCH"
	// CapFowner bypasses permission checks for operations that require the
	// filesystem UID to match the file UID.
	CapFowner Capability = "FOWNER"
	// CapIpcLock allows locking of shared memory segments.
	CapIpcLock Capability = "IPC_LOCK"
	// CapKill allows sending signals to processes of any UID.
	CapKill Capability = "KILL"
	// CapMknod allows creating special files using mknod.
	CapMknod Capability = "MKNOD"
	// CapNetAdmin allows various network administration operations.
	CapNetAdmin Capability = "NET_ADMIN"
	// CapNetBindService allows binding to ports below 1024.
	CapNetBindService Capability = "NET_BIND_SERVICE"
	// CapNetRaw allows use of RAW and PACKET sockets.
	CapNetRaw Capability = "NET_RAW"
	// CapSetfcap allows setting arbitrary capabilities on files.
	CapSetfcap Capability = "SETFCAP"
	// CapSetgid allows manipulating process GIDs and supplementary GID list.
	CapSetgid Capability = "SETGID"
	// CapSetuid allows manipulating process UIDs.
	CapSetuid Capability = "SETUID"
	// CapSysAdmin allows a range of system administration operations.
	CapSysAdmin Capability = "SYS_ADMIN"
	// CapSysModule allows loading and unloading kernel modules.
	CapSysModule Capability = "SYS_MODULE"
	// CapSysPtrace allows tracing arbitrary processes using ptrace.
	CapSysPtrace Capability = "SYS_PTRACE"
	// CapSysRawio allows I/O port operations and raw disk access.
	CapSysRawio Capability = "SYS_RAWIO"
	// CapSysResource allows overriding resource limits.
	CapSysResource Capability = "SYS_RESOURCE"
	// CapSysTime allows setting the system clock.
	CapSysTime Capability = "SYS_TIME"
)

// Security configures the container's security posture. The zero value means
// no overrides: the engine applies its default security settings.
type Security struct {
	// User is the container process identity (e.g. "root", "1000:1000", "nobody").
	// Passed through to the engine as-is.
	User string
	// Groups lists supplementary group names or numeric GIDs.
	// Honored by Docker and Podman; ignored by containerd.
	Groups []string
	// Privileged grants full host device access.
	Privileged bool
	// AddCapabilities lists Linux capabilities to grant beyond the default set.
	AddCapabilities []Capability
	// DropCapabilities lists Linux capabilities to revoke from the default set.
	// Use CapAll to drop all capabilities, then re-add only what is needed.
	DropCapabilities []Capability
	// SecurityOpts are pass-through security options (e.g. "seccomp=unconfined",
	// "label=disable"). Honored by Docker and Podman; ignored by containerd.
	SecurityOpts []string
}

// DNS configures container DNS resolution. The zero value means the engine
// applies its default resolver settings.
type DNS struct {
	// Servers lists nameserver IP addresses (e.g. "8.8.8.8").
	Servers []string
	// Search lists DNS search domains (e.g. "example.com").
	Search []string
	// Options lists resolver options (e.g. "ndots:5").
	Options []string
}
