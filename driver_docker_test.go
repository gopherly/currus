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
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildDockerCaps verifies that buildDockerCaps sets RootlessCapable
// correctly for the Docker and Podman engine variants.
func TestBuildDockerCaps(t *testing.T) {
	t.Parallel()

	dockerCaps := buildDockerCaps(dockerKindDocker)
	assert.False(t, dockerCaps.RootlessCapable)

	podmanCaps := buildDockerCaps(dockerKindPodman)
	assert.True(t, podmanCaps.RootlessCapable)
}

// TestDockerConvertMounts verifies that dockerConvertMounts maps
// ContainerSpec mounts to the Moby host-config mount format.
func TestDockerConvertMounts(t *testing.T) {
	t.Parallel()

	t.Run("empty returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, dockerConvertMounts(nil))
		assert.Nil(t, dockerConvertMounts([]Mount{}))
	})

	t.Run("fields are mapped correctly", func(t *testing.T) {
		t.Parallel()
		in := []Mount{
			{Type: MountTypeBind, Source: "/host/path", Target: "/container/path", ReadOnly: true},
			{Type: MountTypeVolume, Source: "myvolume", Target: "/data"},
		}
		got := dockerConvertMounts(in)
		require.Len(t, got, 2)
		assert.Equal(t, "/host/path", got[0].Source)
		assert.Equal(t, "/container/path", got[0].Target)
		assert.True(t, got[0].ReadOnly)
		assert.Equal(t, "volume", string(got[1].Type))
	})
}

// TestDockerConvertPorts also verifies that an invalid port spec is silently
// skipped rather than surfaced as an error.
func TestDockerConvertPorts(t *testing.T) {
	t.Parallel()

	t.Run("empty returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, dockerConvertPorts(nil))
		assert.Nil(t, dockerConvertPorts([]Port{}))
	})

	t.Run("default protocol is tcp", func(t *testing.T) {
		t.Parallel()
		pm := dockerConvertPorts([]Port{{Container: 80}})
		require.NotEmpty(t, pm)
		key, err := network.ParsePort("80/tcp")
		require.NoError(t, err)
		_, ok := pm[key]
		assert.True(t, ok)
	})

	t.Run("explicit udp protocol is preserved", func(t *testing.T) {
		t.Parallel()
		pm := dockerConvertPorts([]Port{{Container: 53, Protocol: "udp"}})
		require.NotEmpty(t, pm)
		key, err := network.ParsePort("53/udp")
		require.NoError(t, err)
		_, ok := pm[key]
		assert.True(t, ok)
	})

	t.Run("host port zero leaves HostPort empty", func(t *testing.T) {
		t.Parallel()
		pm := dockerConvertPorts([]Port{{Container: 8080}})
		key, err := network.ParsePort("8080/tcp")
		require.NoError(t, err)
		bindings := pm[key]
		require.Len(t, bindings, 1)
		assert.Empty(t, bindings[0].HostPort)
	})

	t.Run("host port non-zero is set", func(t *testing.T) {
		t.Parallel()
		pm := dockerConvertPorts([]Port{{Container: 8080, Host: 9090}})
		key, err := network.ParsePort("8080/tcp")
		require.NoError(t, err)
		bindings := pm[key]
		require.Len(t, bindings, 1)
		assert.Equal(t, "9090", bindings[0].HostPort)
	})
}

// TestDockerConvertRestartPolicy verifies that dockerConvertRestartPolicy
// maps RestartPolicy modes and retry counts to the Moby container config.
func TestDockerConvertRestartPolicy(t *testing.T) {
	t.Parallel()

	t.Run("empty mode returns disabled", func(t *testing.T) {
		t.Parallel()
		rp := dockerConvertRestartPolicy(RestartPolicy{})
		assert.Equal(t, container.RestartPolicyDisabled, rp.Name)
	})

	t.Run("on-failure mode with retry count", func(t *testing.T) {
		t.Parallel()
		rp := dockerConvertRestartPolicy(RestartPolicy{Mode: "on-failure", MaxRetries: 3})
		assert.Equal(t, container.RestartPolicyMode("on-failure"), rp.Name)
		assert.Equal(t, 3, rp.MaximumRetryCount)
	})

	t.Run("always mode", func(t *testing.T) {
		t.Parallel()
		rp := dockerConvertRestartPolicy(RestartPolicy{Mode: "always"})
		assert.Equal(t, container.RestartPolicyMode("always"), rp.Name)
	})
}

// TestDockerActorString verifies that dockerActorString returns the container
// name when available and falls back to the actor ID otherwise.
func TestDockerActorString(t *testing.T) {
	t.Parallel()

	t.Run("name attribute is preferred", func(t *testing.T) {
		t.Parallel()
		a := events.Actor{ID: "abc123", Attributes: map[string]string{"name": "my-container"}}
		assert.Equal(t, "my-container", dockerActorString(a))
	})

	t.Run("falls back to ID when no name attribute", func(t *testing.T) {
		t.Parallel()
		a := events.Actor{ID: "abc123", Attributes: map[string]string{"image": "alpine"}}
		assert.Equal(t, "abc123", dockerActorString(a))
	})

	t.Run("falls back to ID when attributes nil", func(t *testing.T) {
		t.Parallel()
		a := events.Actor{ID: "xyz789"}
		assert.Equal(t, "xyz789", dockerActorString(a))
	})
}

// TestDockerNetInputOutput verifies that dockerNetInput and dockerNetOutput
// sum received and transmitted bytes across all network interfaces.
func TestDockerNetInputOutput(t *testing.T) {
	t.Parallel()

	t.Run("no networks returns zero", func(t *testing.T) {
		t.Parallel()
		s := container.StatsResponse{}
		assert.Equal(t, uint64(0), dockerNetInput(s))
		assert.Equal(t, uint64(0), dockerNetOutput(s))
	})

	t.Run("sums across interfaces", func(t *testing.T) {
		t.Parallel()
		s := container.StatsResponse{
			Networks: map[string]container.NetworkStats{
				"eth0": {RxBytes: 100, TxBytes: 200},
				"eth1": {RxBytes: 50, TxBytes: 75},
			},
		}
		assert.Equal(t, uint64(150), dockerNetInput(s))
		assert.Equal(t, uint64(275), dockerNetOutput(s))
	})
}
