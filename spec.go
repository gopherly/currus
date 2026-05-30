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
	Image      string
	Name       string
	Command    []string
	Args       []string
	Env        []string
	WorkingDir string
	Labels     map[string]string
	Mounts     []Mount
	Ports      []Port
	Resources  Resources
	Restart    RestartPolicy
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
	Type     MountType
	Source   string
	Target   string
	ReadOnly bool
}

// Port describes a single port binding between container and host.
type Port struct {
	Container uint16
	Host      uint16
	Protocol  string
}

// Resources constrains the CPU and memory available to a container.
type Resources struct {
	NanoCPUs    int64
	MemoryBytes int64
}

// RestartPolicy describes when and how many times the engine should restart
// a container after it exits.
type RestartPolicy struct {
	Mode       string
	MaxRetries int
}
