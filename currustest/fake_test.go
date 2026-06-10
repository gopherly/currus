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

package currustest_test

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gopherly.dev/currus"
	"gopherly.dev/currus/currustest"
)

// TestFakeEngine covers methods not exercised by the conformance suite.
func TestFakeEngine(t *testing.T) {
	t.Parallel()
	eng := currustest.New()

	assert.Equal(t, currus.EngineKind("fake"), eng.Kind())
	assert.Equal(t, currus.Caps{}, eng.Capabilities())
	assert.NoError(t, eng.Close())
}

// TestFakeSetLogs also verifies that calling SetLogs for an unknown container
// is a no-op rather than an error.
func TestFakeSetLogs(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	eng := currustest.New()

	id, err := eng.CreateContainer(ctx, currus.ContainerSpec{Image: "alpine"})
	require.NoError(t, err)

	const want = "hello from fake\n"
	eng.SetLogs(id, want)

	rc, err := eng.ContainerLogs(ctx, id, currus.ContainerLogsOpts{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, rc.Close()) })
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, want, string(got))

	// Unknown ID is a no-op; does not panic or error.
	eng.SetLogs("nonexistent", "ignored")
}

// TestFakeRemoveImage verifies that RemoveImage removes a pulled image and
// returns ErrNotFound on a second removal attempt.
func TestFakeRemoveImage(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	eng := currustest.New()

	require.NoError(t, eng.PullImage(ctx, "alpine:latest", currus.PullImageOpts{}))

	require.NoError(t, eng.RemoveImage(ctx, "alpine:latest", currus.RemoveImageOpts{}))

	// Second removal returns not-found.
	err := eng.RemoveImage(ctx, "alpine:latest", currus.RemoveImageOpts{})
	require.Error(t, err)
	assert.ErrorIs(t, err, currus.ErrNotFound)
}

// TestFakeTagImage verifies that TagImage adds a new tag to an existing image
// and returns ErrNotFound when the source image is missing.
func TestFakeTagImage(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	eng := currustest.New()

	require.NoError(t, eng.PullImage(ctx, "alpine:latest", currus.PullImageOpts{}))

	require.NoError(t, eng.TagImage(ctx, "alpine:latest", "myrepo/alpine:v1"))

	// Tagged image is now in the store.
	imgs, err := eng.ListImages(ctx, currus.ListImagesOpts{})
	require.NoError(t, err)
	ids := make([]string, 0, len(imgs))
	for _, img := range imgs {
		ids = append(ids, img.ID)
	}
	assert.Contains(t, ids, "myrepo/alpine:v1")

	// Source not found.
	err = eng.TagImage(ctx, "missing:image", "dst:tag")
	require.Error(t, err)
	assert.ErrorIs(t, err, currus.ErrNotFound)
}

// TestFakeCopyToContainer verifies that CopyToContainer succeeds for an
// existing container and returns ErrNotFound for an unknown container ID.
func TestFakeCopyToContainer(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	eng := currustest.New()

	id, err := eng.CreateContainer(ctx, currus.ContainerSpec{Image: "alpine"})
	require.NoError(t, err)

	require.NoError(t, eng.CopyToContainer(ctx, id, currus.CopyToContainerOpts{DestPath: "/tmp"}))

	err = eng.CopyToContainer(ctx, "no-such-id", currus.CopyToContainerOpts{DestPath: "/tmp"})
	require.Error(t, err)
	assert.ErrorIs(t, err, currus.ErrNotFound)
}

// TestFakeCopyFromContainer verifies that CopyFromContainer returns an empty
// reader for an existing container and ErrNotFound for an unknown container ID.
func TestFakeCopyFromContainer(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	eng := currustest.New()

	id, err := eng.CreateContainer(ctx, currus.ContainerSpec{Image: "alpine"})
	require.NoError(t, err)

	rc, err := eng.CopyFromContainer(ctx, id, currus.CopyFromContainerOpts{SrcPath: "/etc"})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, rc.Close()) })
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Empty(t, data)

	_, err = eng.CopyFromContainer(ctx, "no-such-id", currus.CopyFromContainerOpts{SrcPath: "/etc"})
	require.Error(t, err)
	assert.ErrorIs(t, err, currus.ErrNotFound)
}

// TestFakeContainerState verifies the container state transitions: empty for
// unknown IDs, then created → running → exited across the lifecycle.
func TestFakeContainerState(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	eng := currustest.New()

	assert.Empty(t, eng.ContainerState("nonexistent"))

	id, err := eng.CreateContainer(ctx, currus.ContainerSpec{Image: "alpine"})
	require.NoError(t, err)
	assert.Equal(t, "created", eng.ContainerState(id))

	require.NoError(t, eng.StartContainer(ctx, id))
	assert.Equal(t, "running", eng.ContainerState(id))

	require.NoError(t, eng.StopContainer(ctx, id, currus.StopContainerOpts{}))
	assert.Equal(t, "exited", eng.ContainerState(id))
}

// TestFakeNotFoundErrors verifies that every engine operation returns
// ErrNotFound when given an unknown container or image ID.
func TestFakeNotFoundErrors(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	const ghost = currus.ContainerID("does-not-exist")

	t.Run("CreateContainer empty image", func(t *testing.T) {
		t.Parallel()
		eng := currustest.New()
		_, err := eng.CreateContainer(ctx, currus.ContainerSpec{})
		require.Error(t, err)
		assert.ErrorIs(t, err, currus.ErrInvalidSpec)
	})

	t.Run("StartContainer not found", func(t *testing.T) {
		t.Parallel()
		eng := currustest.New()
		err := eng.StartContainer(ctx, ghost)
		require.Error(t, err)
		assert.ErrorIs(t, err, currus.ErrNotFound)
	})

	t.Run("StopContainer not found", func(t *testing.T) {
		t.Parallel()
		eng := currustest.New()
		err := eng.StopContainer(ctx, ghost, currus.StopContainerOpts{})
		require.Error(t, err)
		assert.ErrorIs(t, err, currus.ErrNotFound)
	})

	t.Run("RemoveContainer not found", func(t *testing.T) {
		t.Parallel()
		eng := currustest.New()
		err := eng.RemoveContainer(ctx, ghost, currus.RemoveContainerOpts{})
		require.Error(t, err)
		assert.ErrorIs(t, err, currus.ErrNotFound)
	})

	t.Run("ContainerLogs not found", func(t *testing.T) {
		t.Parallel()
		eng := currustest.New()
		_, err := eng.ContainerLogs(ctx, ghost, currus.ContainerLogsOpts{})
		require.Error(t, err)
		assert.ErrorIs(t, err, currus.ErrNotFound)
	})

	t.Run("Exec not found", func(t *testing.T) {
		t.Parallel()
		eng := currustest.New()
		_, err := eng.Exec(ctx, ghost, currus.ExecOpts{Cmd: []string{"ls"}})
		require.Error(t, err)
		assert.ErrorIs(t, err, currus.ErrNotFound)
	})

	t.Run("Stats not found", func(t *testing.T) {
		t.Parallel()
		eng := currustest.New()
		_, err := eng.Stats(ctx, ghost, currus.StatsOpts{})
		require.Error(t, err)
		assert.ErrorIs(t, err, currus.ErrNotFound)
	})

	t.Run("WaitContainer not found", func(t *testing.T) {
		t.Parallel()
		eng := currustest.New()
		_, err := eng.WaitContainer(ctx, ghost, currus.WaitContainerOpts{})
		require.Error(t, err)
		assert.ErrorIs(t, err, currus.ErrNotFound)
	})
}

// TestFakeConflictErrors verifies that operations on containers in the wrong
// state return ErrConflict.
func TestFakeConflictErrors(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("StartContainer already running", func(t *testing.T) {
		t.Parallel()
		eng := currustest.New()
		id, err := eng.CreateContainer(ctx, currus.ContainerSpec{Image: "alpine"})
		require.NoError(t, err)
		require.NoError(t, eng.StartContainer(ctx, id))

		err = eng.StartContainer(ctx, id)
		require.Error(t, err)
		assert.ErrorIs(t, err, currus.ErrConflict)
	})

	t.Run("StopContainer not running", func(t *testing.T) {
		t.Parallel()
		eng := currustest.New()
		id, err := eng.CreateContainer(ctx, currus.ContainerSpec{Image: "alpine"})
		require.NoError(t, err)

		err = eng.StopContainer(ctx, id, currus.StopContainerOpts{})
		require.Error(t, err)
		assert.ErrorIs(t, err, currus.ErrConflict)
	})

	t.Run("RemoveContainer running without force", func(t *testing.T) {
		t.Parallel()
		eng := currustest.New()
		id, err := eng.CreateContainer(ctx, currus.ContainerSpec{Image: "alpine"})
		require.NoError(t, err)
		require.NoError(t, eng.StartContainer(ctx, id))

		err = eng.RemoveContainer(ctx, id, currus.RemoveContainerOpts{Force: false})
		require.Error(t, err)
		assert.ErrorIs(t, err, currus.ErrConflict)
	})
}

// TestFakePingEndpointEvents verifies the trivial methods that are not
// exercised by the conformance suite's external-package attribution.
func TestFakePingEndpointEvents(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	eng := currustest.New()

	t.Run("Ping always succeeds", func(t *testing.T) {
		t.Parallel()
		assert.NoError(t, eng.Ping(ctx))
	})

	t.Run("Endpoint returns synthetic host", func(t *testing.T) {
		t.Parallel()
		ep := eng.Endpoint()
		assert.Equal(t, "unix:///var/run/fake.sock", ep.Host)
	})

	t.Run("Events returns a closed channel", func(t *testing.T) {
		t.Parallel()
		ch, err := eng.Events(ctx)
		require.NoError(t, err)
		require.NotNil(t, ch)
		// Channel should be closed immediately — draining must not block.
		for range ch {
		}
	})
}

// TestFakeInspect verifies that Inspect returns the full spec fields for a
// known container and ErrNotFound for an unknown ID.
func TestFakeInspect(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	eng := currustest.New()

	spec := currus.ContainerSpec{
		Image:      "alpine:latest",
		Name:       "my-ctr",
		Command:    []string{"/bin/sh"},
		Args:       []string{"-c", "sleep 1"},
		Env:        []string{"FOO=bar"},
		WorkingDir: "/app",
		Labels:     map[string]string{"env": "test"},
		Hostname:   "box",
		ExtraHosts: []string{"db:10.0.0.1"},
		Init:       true,
		Security: currus.Security{
			User:             "1000:1000",
			Groups:           []string{"docker"},
			Privileged:       false,
			AddCapabilities:  []currus.Capability{currus.CapNetBindService},
			DropCapabilities: []currus.Capability{currus.CapAll},
			SecurityOpts:     []string{"no-new-privileges"},
		},
		DNS: currus.DNS{
			Servers: []string{"8.8.8.8"},
			Search:  []string{"example.com"},
			Options: []string{"ndots:5"},
		},
	}
	id, err := eng.CreateContainer(ctx, spec)
	require.NoError(t, err)

	t.Run("known container returns correct info", func(t *testing.T) {
		t.Parallel()
		info, infoErr := eng.Inspect(ctx, id)
		require.NoError(t, infoErr)
		assert.Equal(t, id, info.ID)
		assert.Equal(t, spec.Name, info.Name)
		assert.Equal(t, spec.Image, info.Image)
		assert.Equal(t, spec.Labels, info.Labels)
		assert.Equal(t, spec.WorkingDir, info.WorkingDir)
		assert.Equal(t, spec.Env, info.Env)
		assert.False(t, info.State.Running)
		// Security
		assert.Equal(t, spec.Security.User, info.Security.User)
		assert.Equal(t, spec.Security.Groups, info.Security.Groups)
		assert.Equal(t, spec.Security.Privileged, info.Security.Privileged)
		assert.Equal(t, spec.Security.AddCapabilities, info.Security.AddCapabilities)
		assert.Equal(t, spec.Security.DropCapabilities, info.Security.DropCapabilities)
		assert.Equal(t, spec.Security.SecurityOpts, info.Security.SecurityOpts)
		// DNS
		assert.Equal(t, spec.DNS.Servers, info.DNS.Servers)
		assert.Equal(t, spec.DNS.Search, info.DNS.Search)
		assert.Equal(t, spec.DNS.Options, info.DNS.Options)
		// Hostname, ExtraHosts, Init
		assert.Equal(t, spec.Hostname, info.Hostname)
		assert.Equal(t, spec.ExtraHosts, info.ExtraHosts)
		assert.Equal(t, spec.Init, info.Init)
	})

	t.Run("running container shows Running=true", func(t *testing.T) {
		t.Parallel()
		ctx2 := t.Context()
		eng2 := currustest.New()
		id2, createErr := eng2.CreateContainer(ctx2, currus.ContainerSpec{Image: "alpine"})
		require.NoError(t, createErr)
		require.NoError(t, eng2.StartContainer(ctx2, id2))
		info, inspErr := eng2.Inspect(ctx2, id2)
		require.NoError(t, inspErr)
		assert.True(t, info.State.Running)
	})

	t.Run("unknown container returns ErrNotFound", func(t *testing.T) {
		t.Parallel()
		_, notFoundErr := eng.Inspect(ctx, currus.ContainerID("ghost"))
		require.Error(t, notFoundErr)
		assert.ErrorIs(t, notFoundErr, currus.ErrNotFound)
	})
}

// TestFakeExec verifies that Exec echoes the cmd as stdout when AttachStdout
// is true, and returns ErrNotFound for an unknown container.
func TestFakeExec(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	eng := currustest.New()

	id, err := eng.CreateContainer(ctx, currus.ContainerSpec{Image: "alpine"})
	require.NoError(t, err)
	require.NoError(t, eng.StartContainer(ctx, id))

	t.Run("with stdout and stderr attached", func(t *testing.T) {
		t.Parallel()
		result, execErr := eng.Exec(ctx, id, currus.ExecOpts{
			Cmd:          []string{"echo", "hello"},
			AttachStdout: true,
			AttachStderr: true,
		})
		require.NoError(t, execErr)
		assert.Equal(t, 0, result.ExitCode)
		require.NotNil(t, result.Stdout)
		require.NotNil(t, result.Stderr)
		buf := new(strings.Builder)
		_, copyErr := io.Copy(buf, result.Stdout)
		require.NoError(t, copyErr)
		assert.Equal(t, "echo hello\n", buf.String())
	})

	t.Run("without stdout attached returns nil Stdout", func(t *testing.T) {
		t.Parallel()
		result, execErr := eng.Exec(ctx, id, currus.ExecOpts{
			Cmd:          []string{"ls"},
			AttachStdout: false,
			AttachStderr: false,
		})
		require.NoError(t, execErr)
		assert.Nil(t, result.Stdout)
		assert.Nil(t, result.Stderr)
	})

	t.Run("unknown container returns ErrNotFound", func(t *testing.T) {
		t.Parallel()
		_, notFoundErr := eng.Exec(ctx, "ghost", currus.ExecOpts{Cmd: []string{"ls"}})
		require.Error(t, notFoundErr)
		assert.ErrorIs(t, notFoundErr, currus.ErrNotFound)
	})
}

// TestFakeNetworkerCRUD exercises the full create/list/connect/members/
// disconnect/remove lifecycle of the Fake's Networker implementation.
func TestFakeNetworkerCRUD(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	eng := currustest.New()

	// Create network.
	netID, err := eng.CreateNetwork(ctx, "my-net", currus.CreateNetworkOpts{Driver: "bridge"})
	require.NoError(t, err)
	assert.NotEmpty(t, string(netID))

	// ListNetworks should include the new network.
	nets, err := eng.ListNetworks(ctx, currus.ListNetworksOpts{})
	require.NoError(t, err)
	var found bool
	for _, n := range nets {
		if n.ID == netID {
			found = true
			assert.Equal(t, "my-net", n.Name)
			assert.Equal(t, "bridge", n.Driver)
		}
	}
	assert.Truef(t, found, "created network not in ListNetworks")

	// Create a container and connect it.
	ctrID, err := eng.CreateContainer(ctx, currus.ContainerSpec{Image: "alpine"})
	require.NoError(t, err)

	require.NoError(t, eng.ConnectContainer(ctx, netID, ctrID, currus.ConnectOpts{}))

	members := eng.NetworkMembers(netID)
	assert.Contains(t, members, ctrID)

	// Disconnect.
	require.NoError(t, eng.DisconnectContainer(ctx, netID, ctrID, currus.DisconnectOpts{}))
	members = eng.NetworkMembers(netID)
	assert.NotContains(t, members, ctrID)

	// ConnectContainer: unknown container returns ErrNotFound.
	err = eng.ConnectContainer(ctx, netID, "ghost", currus.ConnectOpts{})
	require.Error(t, err)
	assert.ErrorIs(t, err, currus.ErrNotFound)

	// DisconnectContainer: unknown container returns ErrNotFound.
	err = eng.DisconnectContainer(ctx, netID, "ghost", currus.DisconnectOpts{})
	require.Error(t, err)
	assert.ErrorIs(t, err, currus.ErrNotFound)

	// Remove the network.
	require.NoError(t, eng.RemoveNetwork(ctx, netID, currus.RemoveNetworkOpts{}))

	// Second remove returns ErrNotFound.
	err = eng.RemoveNetwork(ctx, netID, currus.RemoveNetworkOpts{})
	require.Error(t, err)
	assert.ErrorIs(t, err, currus.ErrNotFound)
}

// TestFakeVolumerCRUD exercises the full create/list/remove lifecycle of the
// Fake's Volumer implementation.
func TestFakeVolumerCRUD(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	eng := currustest.New()

	// Create volume.
	volID, err := eng.CreateVolume(ctx, "data", currus.CreateVolumeOpts{Driver: "local"})
	require.NoError(t, err)
	assert.NotEmpty(t, string(volID))

	// ListVolumes should include the new volume.
	vols, err := eng.ListVolumes(ctx, currus.ListVolumesOpts{})
	require.NoError(t, err)
	var found bool
	for _, v := range vols {
		if v.ID == volID {
			found = true
			assert.Equal(t, "local", v.Driver)
			assert.NotEmpty(t, v.Mountpoint)
		}
	}
	assert.Truef(t, found, "created volume not in ListVolumes")

	// Remove the volume.
	require.NoError(t, eng.RemoveVolume(ctx, volID, currus.RemoveVolumeOpts{}))

	// Second remove returns ErrNotFound.
	err = eng.RemoveVolume(ctx, volID, currus.RemoveVolumeOpts{})
	require.Error(t, err)
	assert.ErrorIs(t, err, currus.ErrNotFound)
}

// TestFakeListContainersFilter verifies that ListContainers respects the All
// flag, hiding non-running containers when All is false.
func TestFakeListContainersFilter(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	eng := currustest.New()

	// Create a container but do not start it — state is "created".
	id, err := eng.CreateContainer(ctx, currus.ContainerSpec{Image: "alpine"})
	require.NoError(t, err)

	// All=false should exclude the non-running container.
	list, err := eng.ListContainers(ctx, currus.ListContainersOpts{All: false})
	require.NoError(t, err)
	for _, c := range list {
		assert.NotEqualf(t, id, c.ID, "non-running container should be filtered with All=false")
	}

	// All=true should include it.
	list, err = eng.ListContainers(ctx, currus.ListContainersOpts{All: true})
	require.NoError(t, err)
	var found bool
	for _, c := range list {
		if c.ID == id {
			found = true
			break
		}
	}
	assert.Truef(t, found, "created container should appear with All=true")
}
