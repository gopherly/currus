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

	assert.Equal(t, currus.EngineKind("fake"), eng.Engine())
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
		assert.ErrorIs(t, err, currus.ErrNotFound)
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
