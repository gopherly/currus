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

// Package conformance provides the shared behavioral test suite that verifies
// any [currus.Engine] implementation against the neutral contract defined by
// the currus package.
//
// The suite is the executable specification of the neutral contract. The same
// tests run against:
//
//   - The [currustest] in-memory fake (unit layer, always on, no daemon).
//   - Real engine daemons (integration layer, gated by //go:build integration
//     and CURRUS_TEST_ENGINE=docker|podman|containerd).
//
// Driver maintainers call [Run] with a factory function that returns an
// [currus.Engine] for each sub-test. Engines that implement optional
// capability interfaces ([currus.Logger], [currus.Execer], [currus.Inspector],
// [currus.Stater], [currus.Waiter], [currus.Eventer]) are tested for those
// as well.
//
// Usage (unit layer, no daemon):
//
//	func TestConformance(t *testing.T) {
//	    conformance.Run(t, func(t *testing.T) currus.Engine {
//	        return currustest.New()
//	    })
//	}
//
// Usage (integration layer):
//
//	//go:build integration
//
//	func TestConformanceIntegration(t *testing.T) {
//	    conformance.Run(t, func(t *testing.T) currus.Engine {
//	        eng, err := currus.New(context.Background())
//	        if err != nil {
//	            t.Skip("no reachable engine:", err)
//	        }
//	        t.Cleanup(func() { _ = eng.Close() })
//	        return eng
//	    })
//	}
package conformance

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gopherly.dev/currus"
)

const testImage = "docker.io/library/busybox:latest"

// Run runs the full conformance suite against the engine returned by newEngine.
// newEngine is called once per sub-test. If the engine needs cleanup, register
// it with t.Cleanup inside newEngine.
//
//nolint:gocognit,cyclop // deliberate flat table of independent capability subtests; splitting would scatter the suite
func Run(t *testing.T, newEngine func(t *testing.T) currus.Engine) {
	t.Helper()

	// name builds a resource name that is unique to this Run invocation. A
	// run-scoped suffix keeps parallel subtests from colliding and, crucially,
	// prevents leftover resources from an interrupted earlier run (whose
	// t.Cleanup never executed) from blocking the next run with a name
	// conflict. The suffix is fixed within a single Run, so subtests that
	// deliberately reuse a name to assert a conflict still work.
	runID := strconv.FormatInt(time.Now().UnixNano(), 36)
	name := func(suffix string) string {
		return "currus-conformance-" + runID + "-" + suffix
	}

	t.Run("Ping", func(t *testing.T) {
		t.Parallel()
		eng := newEngine(t)
		require.NoError(t, eng.Ping(t.Context()))
	})

	t.Run("PullImage", func(t *testing.T) {
		t.Parallel()
		eng := newEngine(t)
		err := eng.PullImage(t.Context(), testImage, currus.PullImageOpts{})
		require.NoError(t, err)
	})

	t.Run("CreateAndRemoveContainer", func(t *testing.T) {
		t.Parallel()
		eng := newEngine(t)
		ctx := t.Context()

		_ = eng.PullImage(ctx, testImage, currus.PullImageOpts{}) //nolint:errcheck // best-effort: image may already be present

		id, err := eng.CreateContainer(ctx, currus.ContainerSpec{
			Image: testImage,
			Name:  name("create"),
		})
		require.NoError(t, err)
		require.NotEmpty(t, string(id))

		_, err = eng.CreateContainer(ctx, currus.ContainerSpec{
			Image: testImage,
			Name:  name("create"),
		})
		if err != nil {
			assert.True(t,
				errors.Is(err, currus.ErrAlreadyExists) || errors.Is(err, currus.ErrConflict),
				"expected ErrAlreadyExists or ErrConflict on duplicate name, got: %v", err)
		}

		require.NoError(t, eng.RemoveContainer(ctx, id, currus.RemoveContainerOpts{Force: true}))

		err = eng.RemoveContainer(ctx, id, currus.RemoveContainerOpts{})
		assert.True(t, errors.Is(err, currus.ErrNotFound),
			"expected ErrNotFound on second remove, got: %v", err)
	})

	t.Run("StartStopContainer", func(t *testing.T) {
		t.Parallel()
		eng := newEngine(t)
		ctx := t.Context()

		_ = eng.PullImage(ctx, testImage, currus.PullImageOpts{}) //nolint:errcheck // best-effort: image may already be present

		id, err := eng.CreateContainer(ctx, currus.ContainerSpec{
			Image:   testImage,
			Name:    name("startstop"),
			Command: []string{"sleep"},
			Args:    []string{"30"},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = eng.RemoveContainer(ctx, id, currus.RemoveContainerOpts{Force: true}) //nolint:errcheck // best-effort cleanup
		})

		require.NoError(t, eng.StartContainer(ctx, id))
		require.NoError(t, eng.StopContainer(ctx, id, currus.StopContainerOpts{}))
	})

	t.Run("ListContainers", func(t *testing.T) {
		t.Parallel()
		eng := newEngine(t)
		ctx := t.Context()

		_ = eng.PullImage(ctx, testImage, currus.PullImageOpts{}) //nolint:errcheck // best-effort: image may already be present

		id, err := eng.CreateContainer(ctx, currus.ContainerSpec{
			Image: testImage,
			Name:  name("list"),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = eng.RemoveContainer(ctx, id, currus.RemoveContainerOpts{Force: true}) //nolint:errcheck // best-effort cleanup
		})

		// All=true should include the created (stopped) container.
		containers, err := eng.ListContainers(ctx, currus.ListContainersOpts{All: true})
		require.NoError(t, err)

		found := false
		for _, c := range containers {
			if c.ID == id {
				found = true
				break
			}
		}
		assert.True(t, found, "created container %s not found in ListContainers", id)
	})

	t.Run("ErrNotFoundOnMissingContainer", func(t *testing.T) {
		t.Parallel()
		eng := newEngine(t)
		ctx := t.Context()
		bogus := currus.ContainerID("currus-nonexistent-12345")

		err := eng.StartContainer(ctx, bogus)
		assert.True(t, errors.Is(err, currus.ErrNotFound),
			"StartContainer on missing id: expected ErrNotFound, got %v", err)

		err = eng.StopContainer(ctx, bogus, currus.StopContainerOpts{})
		assert.True(t, errors.Is(err, currus.ErrNotFound),
			"StopContainer on missing id: expected ErrNotFound, got %v", err)

		err = eng.RemoveContainer(ctx, bogus, currus.RemoveContainerOpts{})
		assert.True(t, errors.Is(err, currus.ErrNotFound),
			"RemoveContainer on missing id: expected ErrNotFound, got %v", err)
	})

	t.Run("LoggerCapability", func(t *testing.T) {
		t.Parallel()
		eng := newEngine(t)

		lg, ok := eng.(currus.Logger)
		if !ok {
			t.Skip("engine does not implement currus.Logger; skipping log tests")
		}

		ctx := t.Context()
		_ = eng.PullImage(ctx, testImage, currus.PullImageOpts{}) //nolint:errcheck // best-effort: image may already be present

		id, err := eng.CreateContainer(ctx, currus.ContainerSpec{
			Image:   testImage,
			Name:    name("logs"),
			Command: []string{"echo"},
			Args:    []string{"hello currus"},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = eng.RemoveContainer(ctx, id, currus.RemoveContainerOpts{Force: true}) //nolint:errcheck // best-effort cleanup
		})

		require.NoError(t, eng.StartContainer(ctx, id))

		rc, err := lg.ContainerLogs(ctx, id, currus.ContainerLogsOpts{Follow: false, Tail: 10})
		require.NoError(t, err)
		defer rc.Close() //nolint:errcheck // test cleanup

		buf := new(strings.Builder)
		_, err = io.Copy(buf, rc)
		require.NoError(t, err)
		// Logs may be empty for very short-lived containers; just assert no error.
		t.Logf("logs: %q", buf.String())
	})

	t.Run("ExecerCapability", func(t *testing.T) {
		t.Parallel()
		eng := newEngine(t)

		ex, ok := eng.(currus.Execer)
		if !ok {
			t.Skip("engine does not implement currus.Execer; skipping exec tests")
		}

		ctx := t.Context()
		_ = eng.PullImage(ctx, testImage, currus.PullImageOpts{}) //nolint:errcheck // best-effort: image may already be present

		id, err := eng.CreateContainer(ctx, currus.ContainerSpec{
			Image:   testImage,
			Name:    name("exec"),
			Command: []string{"sleep"},
			Args:    []string{"30"},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = eng.RemoveContainer(ctx, id, currus.RemoveContainerOpts{Force: true}) //nolint:errcheck // best-effort cleanup
		})

		require.NoError(t, eng.StartContainer(ctx, id))

		result, err := ex.Exec(ctx, id, currus.ExecOpts{
			Cmd:          []string{"echo", "hello"},
			AttachStdout: true,
			AttachStderr: true,
		})
		require.NoError(t, err)
		assert.Equal(t, 0, result.ExitCode, "exec exit code should be 0")
	})

	t.Run("InspectorCapability", func(t *testing.T) {
		t.Parallel()
		eng := newEngine(t)

		ins, ok := eng.(currus.Inspector)
		if !ok {
			t.Skip("engine does not implement currus.Inspector; skipping inspect tests")
		}

		ctx := t.Context()
		_ = eng.PullImage(ctx, testImage, currus.PullImageOpts{}) //nolint:errcheck // best-effort: image may already be present

		id, err := eng.CreateContainer(ctx, currus.ContainerSpec{
			Image: testImage,
			Name:  name("inspect"),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = eng.RemoveContainer(ctx, id, currus.RemoveContainerOpts{Force: true}) //nolint:errcheck // best-effort cleanup
		})

		info, err := ins.Inspect(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, id, info.ID, "inspect ID mismatch")
		assert.NotEmpty(t, info.Image, "inspect image should not be empty")

		_, err = ins.Inspect(ctx, currus.ContainerID("currus-nonexistent-inspect-99"))
		assert.True(t, errors.Is(err, currus.ErrNotFound),
			"inspect missing container: expected ErrNotFound, got %v", err)
	})

	t.Run("StaterCapability", func(t *testing.T) {
		t.Parallel()
		eng := newEngine(t)

		st, ok := eng.(currus.Stater)
		if !ok {
			t.Skip("engine does not implement currus.Stater; skipping stats tests")
		}

		ctx := t.Context()
		_ = eng.PullImage(ctx, testImage, currus.PullImageOpts{}) //nolint:errcheck // best-effort: image may already be present

		id, err := eng.CreateContainer(ctx, currus.ContainerSpec{
			Image:   testImage,
			Name:    name("stats"),
			Command: []string{"sleep"},
			Args:    []string{"30"},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = eng.RemoveContainer(ctx, id, currus.RemoveContainerOpts{Force: true}) //nolint:errcheck // best-effort cleanup
		})

		require.NoError(t, eng.StartContainer(ctx, id))

		stats, err := st.Stats(ctx, id, currus.StatsOpts{})
		require.NoError(t, err)
		// Stats values can be 0 for a newly started container.
		t.Logf("stats: cpu=%.2f%% mem=%d/%d net_in=%d net_out=%d",
			stats.CPUPercent, stats.MemoryUsage, stats.MemoryLimit,
			stats.NetworkInput, stats.NetworkOutput)
	})

	t.Run("WaiterCapability", func(t *testing.T) {
		t.Parallel()
		eng := newEngine(t)

		wt, ok := eng.(currus.Waiter)
		if !ok {
			t.Skip("engine does not implement currus.Waiter; skipping wait tests")
		}

		ctx := t.Context()
		_ = eng.PullImage(ctx, testImage, currus.PullImageOpts{}) //nolint:errcheck // best-effort: image may already be present

		// Use a container that exits quickly.
		id, err := eng.CreateContainer(ctx, currus.ContainerSpec{
			Image:   testImage,
			Name:    name("wait"),
			Command: []string{"echo"},
			Args:    []string{"done"},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = eng.RemoveContainer(ctx, id, currus.RemoveContainerOpts{Force: true}) //nolint:errcheck // best-effort cleanup
		})

		waitCh, err := wt.WaitContainer(ctx, id, currus.WaitContainerOpts{Condition: "next-exit"})
		require.NoError(t, err)

		require.NoError(t, eng.StartContainer(ctx, id))

		result := <-waitCh
		assert.Empty(t, result.Error, "wait result should have no error")
		t.Logf("wait result: exit_code=%d", result.StatusCode)
	})

	t.Run("ImagerCapability", func(t *testing.T) {
		t.Parallel()
		eng := newEngine(t)

		img, ok := eng.(currus.Imager)
		if !ok {
			t.Skip("engine does not implement currus.Imager; skipping image tests")
		}

		ctx := t.Context()
		_ = eng.PullImage(ctx, testImage, currus.PullImageOpts{}) //nolint:errcheck // best-effort: image may already be present

		images, err := img.ListImages(ctx, currus.ListImagesOpts{All: true})
		require.NoError(t, err)
		t.Logf("images: %d", len(images))
	})

	t.Run("NetworkerCapability", func(t *testing.T) {
		t.Parallel()
		eng := newEngine(t)

		nw, ok := eng.(currus.Networker)
		if !ok {
			t.Skip("engine does not implement currus.Networker; skipping network tests")
		}

		ctx := t.Context()

		id, err := nw.CreateNetwork(ctx, name("net"), currus.CreateNetworkOpts{Driver: "bridge"})
		require.NoError(t, err)
		require.NotEmpty(t, string(id))

		nets, err := nw.ListNetworks(ctx, currus.ListNetworksOpts{})
		require.NoError(t, err)

		found := false
		for _, n := range nets {
			if n.ID == id {
				found = true
				break
			}
		}
		assert.True(t, found, "created network %s not found in ListNetworks", id)

		require.NoError(t, nw.RemoveNetwork(ctx, id, currus.RemoveNetworkOpts{}))
	})

	t.Run("VolumerCapability", func(t *testing.T) {
		t.Parallel()
		eng := newEngine(t)

		vol, ok := eng.(currus.Volumer)
		if !ok {
			t.Skip("engine does not implement currus.Volumer; skipping volume tests")
		}

		ctx := t.Context()

		id, err := vol.CreateVolume(ctx, name("vol"), currus.CreateVolumeOpts{})
		require.NoError(t, err)
		require.NotEmpty(t, string(id))

		vols, err := vol.ListVolumes(ctx, currus.ListVolumesOpts{})
		require.NoError(t, err)

		found := false
		for _, v := range vols {
			if v.ID == id {
				found = true
				break
			}
		}
		assert.True(t, found, "created volume %s not found in ListVolumes", id)

		require.NoError(t, vol.RemoveVolume(ctx, id, currus.RemoveVolumeOpts{Force: true}))
	})

	t.Run("EventerCapability", func(t *testing.T) {
		t.Parallel()
		eng := newEngine(t)

		ev, ok := eng.(currus.Eventer)
		if !ok {
			t.Skip("engine does not implement currus.Eventer; skipping events tests")
		}

		ctx, cancel := context.WithCancel(t.Context())

		ch, err := ev.Events(ctx)
		require.NoError(t, err)
		require.NotNil(t, ch)

		// Cancel immediately; the channel must close or drain gracefully.
		cancel()
		for range ch {
		}
	})
}
