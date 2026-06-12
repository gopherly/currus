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
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	containertypes "github.com/moby/moby/api/types/container"
	eventtypes "github.com/moby/moby/api/types/events"
	imagetypes "github.com/moby/moby/api/types/image"
	networktypes "github.com/moby/moby/api/types/network"
	volumetypes "github.com/moby/moby/api/types/volume"
)

// newMockDockerDaemon starts an [httptest.Server] that simulates a Docker
// daemon and returns a *dockerEngine pointing at it. The handlers map uses
// patterns like "GET /containers/json" (without version prefix). The server
// automatically strips the Docker API version prefix (/v1.XX) before
// dispatching.
//
// A default /_ping handler is always registered so the client can negotiate
// the API version.
func newMockDockerDaemon(t *testing.T, handlers map[string]http.HandlerFunc) *dockerEngine {
	t.Helper()

	inner := http.NewServeMux()
	for pattern, h := range handlers {
		inner.HandleFunc(pattern, h)
	}

	// /_ping is required for Ping() calls and version negotiation.
	if _, ok := handlers["HEAD /_ping"]; !ok {
		inner.HandleFunc("HEAD /_ping", mockPingHandler)
	}
	if _, ok := handlers["GET /_ping"]; !ok {
		inner.HandleFunc("GET /_ping", mockPingHandler)
	}

	// Version-stripping middleware: the moby client prefixes all paths with
	// /vMAJOR.MINOR (e.g. /v1.54/containers/json). The mux registered above
	// uses un-versioned paths, so we strip the prefix before dispatching.
	outer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasPrefix(path, "/v") {
			if idx := strings.Index(path[2:], "/"); idx >= 0 {
				path = path[2+idx:]
			}
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = path
		inner.ServeHTTP(w, r2)
	})

	srv := httptest.NewServer(outer)
	t.Cleanup(srv.Close)

	addr := srv.Listener.Addr().String()
	cli, err := client.New(
		client.WithHost("tcp://"+addr),
		client.WithAPIVersion("1.54"),
		client.WithHTTPClient(srv.Client()),
	)
	require.NoError(t, err)

	return &dockerEngine{
		cli:    cli,
		kind:   Docker,
		logger: slog.Default(),
	}
}

func mockPingHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Api-Version", "1.54")
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK")) //nolint:errcheck
}

func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic("test mock: encode JSON response: " + err.Error())
	}
}

// TestDockerEngineKindAndCapabilities verifies that Kind, Capabilities, and
// Endpoint return the values set at construction time without making HTTP calls.
func TestDockerEngineKindAndCapabilities(t *testing.T) {
	t.Parallel()

	e := &dockerEngine{
		kind:         Docker,
		caps:         Caps{Rootless: true},
		host:         "tcp://127.0.0.1:0",
		daemonSocket: "/var/run/docker.sock",
		logger:       slog.Default(),
	}

	assert.Equal(t, Docker, e.Kind())
	assert.True(t, e.Capabilities().Rootless)
	ep := e.Endpoint()
	assert.Equal(t, "tcp://127.0.0.1:0", ep.Host)
	assert.Equal(t, "/var/run/docker.sock", ep.DaemonSocket)
}

// TestDockerEnginePing verifies that Ping reaches the mock daemon.
func TestDockerEnginePing(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, nil)
	require.NoError(t, e.Ping(t.Context()))
}

// TestDockerEngineClose verifies that Close releases the HTTP client.
func TestDockerEngineClose(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, nil)
	assert.NoError(t, e.Close())
}

// TestDockerEnginePullImage verifies that PullImage posts /images/create and
// waits for the JSON-stream response without error.
func TestDockerEnginePullImage(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"POST /images/create": func(w http.ResponseWriter, _ *http.Request) {
			// Return a minimal JSON stream message to simulate a pull.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"Pull complete"}` + "\n")) //nolint:errcheck
		},
	})

	err := e.PullImage(t.Context(), "nginx:latest", PullImageOpts{})
	require.NoError(t, err)
}

// TestDockerEnginePullImageInvalidPlatform verifies that an invalid platform
// string is rejected before any HTTP call is made.
func TestDockerEnginePullImageInvalidPlatform(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, nil)
	err := e.PullImage(t.Context(), "nginx:latest", PullImageOpts{Platform: "not/a/valid/platform"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidSpec)
}

// TestDockerEngineCreateContainer verifies that CreateContainer posts
// /containers/create and returns the container ID from the response.
func TestDockerEngineCreateContainer(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"POST /containers/create": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusCreated, containertypes.CreateResponse{
				ID:       "abc123",
				Warnings: []string{},
			})
		},
	})

	id, err := e.CreateContainer(t.Context(), ContainerSpec{Image: "nginx:latest"})
	require.NoError(t, err)
	assert.Equal(t, ContainerID("abc123"), id)
}

// TestDockerEngineStartContainer verifies that StartContainer posts the
// correct endpoint and returns no error on HTTP 204.
func TestDockerEngineStartContainer(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"POST /containers/abc123/start": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	})

	require.NoError(t, e.StartContainer(t.Context(), "abc123"))
}

// TestDockerEngineStopContainer verifies that StopContainer posts the
// correct endpoint and returns no error on HTTP 204.
func TestDockerEngineStopContainer(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"POST /containers/abc123/stop": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	})

	require.NoError(t, e.StopContainer(t.Context(), "abc123", StopContainerOpts{Timeout: 5 * time.Second}))
}

// TestDockerEngineRemoveContainer verifies that RemoveContainer deletes the
// correct endpoint and returns no error on HTTP 204.
func TestDockerEngineRemoveContainer(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"DELETE /containers/abc123": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	})

	require.NoError(t, e.RemoveContainer(t.Context(), "abc123", RemoveContainerOpts{Force: true}))
}

// TestDockerEngineListContainers verifies that ListContainers decodes the
// container list from the mock response.
func TestDockerEngineListContainers(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"GET /containers/json": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, []containertypes.Summary{
				{
					ID:    "abc123",
					Names: []string{"/mycontainer"},
					Image: "nginx:latest",
					State: "running",
				},
			})
		},
	})

	containers, err := e.ListContainers(t.Context(), ListContainersOpts{All: true})
	require.NoError(t, err)
	require.Len(t, containers, 1)
	assert.Equal(t, ContainerID("abc123"), containers[0].ID)
	assert.Equal(t, "mycontainer", containers[0].Name)
	assert.Equal(t, "nginx:latest", containers[0].Image)
}

// TestDockerEngineInspect verifies that Inspect decodes the container inspect
// response into a ContainerInfo.
func TestDockerEngineInspect(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"GET /containers/abc123/json": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, containertypes.InspectResponse{
				ID:   "abc123",
				Name: "/mycontainer",
				HostConfig: &containertypes.HostConfig{
					Privileged: false,
				},
				State: &containertypes.State{
					Running: true,
				},
				Config: &containertypes.Config{
					Image: "nginx:latest",
				},
			})
		},
	})

	info, err := e.Inspect(t.Context(), "abc123")
	require.NoError(t, err)
	assert.Equal(t, ContainerID("abc123"), info.ID)
	assert.Equal(t, "mycontainer", info.Name)
	assert.Equal(t, "nginx:latest", info.Image)
	assert.True(t, info.State.Running)
}

// TestDockerEngineContainerLogs covers all ContainerLogs code paths:
// the TTY raw-stream path, the non-TTY demuxed path (exercising demuxCloser),
// and the Tail option.
func TestDockerEngineContainerLogs(t *testing.T) {
	t.Parallel()

	t.Run("TTY raw stream", func(t *testing.T) {
		t.Parallel()

		e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
			"GET /containers/abc123/json": func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, containertypes.InspectResponse{
					ID:     "abc123",
					Config: &containertypes.Config{Tty: true},
				})
			},
			"GET /containers/abc123/logs": func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("hello from container\n")) //nolint:errcheck
			},
		})

		rc, err := e.ContainerLogs(t.Context(), "abc123", ContainerLogsOpts{})
		require.NoError(t, err)
		defer func() { assert.NoError(t, rc.Close()) }()

		data, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, "hello from container\n", string(data))
	})

	t.Run("no TTY demuxed stream", func(t *testing.T) {
		t.Parallel()

		payload := []byte("log line from stdout\n")
		var hdr [8]byte
		hdr[0] = 1                                                // stdout stream
		binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload))) //nolint:gosec
		muxed := append(hdr[:], payload...)

		e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
			"GET /containers/mux1/json": func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, containertypes.InspectResponse{
					ID:     "mux1",
					Config: &containertypes.Config{Tty: false},
				})
			},
			"GET /containers/mux1/logs": func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(muxed) //nolint:errcheck
			},
		})

		rc, err := e.ContainerLogs(t.Context(), "mux1", ContainerLogsOpts{})
		require.NoError(t, err)

		data, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, string(payload), string(data))
		require.NoError(t, rc.Close())
	})

	t.Run("Tail option forwarded", func(t *testing.T) {
		t.Parallel()

		var gotTail string

		e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
			"GET /containers/tail1/json": func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, containertypes.InspectResponse{
					ID:     "tail1",
					Config: &containertypes.Config{Tty: true},
				})
			},
			"GET /containers/tail1/logs": func(w http.ResponseWriter, r *http.Request) {
				gotTail = r.URL.Query().Get("tail")
				w.WriteHeader(http.StatusOK)
			},
		})

		rc, err := e.ContainerLogs(t.Context(), "tail1", ContainerLogsOpts{Tail: 50})
		require.NoError(t, err)
		require.NoError(t, rc.Close())

		assert.Equal(t, "50", gotTail)
	})
}

// TestDockerEngineStats verifies that Stats returns a ContainerStats decoded
// from the mock daemon response.
func TestDockerEngineStats(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"GET /containers/abc123/stats": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, containertypes.StatsResponse{
				MemoryStats: containertypes.MemoryStats{
					Usage: 1024 * 1024,
					Limit: 512 * 1024 * 1024,
				},
			})
		},
	})

	stats, err := e.Stats(t.Context(), "abc123", StatsOpts{})
	require.NoError(t, err)
	assert.Equal(t, uint64(1024*1024), stats.MemoryUsage)
	assert.Equal(t, uint64(512*1024*1024), stats.MemoryLimit)
}

// TestDockerEngineExec verifies that Exec creates an exec instance, attaches
// to it via the mock daemon using HTTP hijacking, and returns the output.
func TestDockerEngineExec(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"POST /containers/abc123/exec": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusCreated, map[string]string{"Id": "exec1"})
		},
		// ExecAttach uses HTTP Upgrade / hijacking.
		"POST /exec/exec1/start": func(w http.ResponseWriter, r *http.Request) {
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "hijacking not supported", http.StatusInternalServerError)
				return
			}
			conn, bufrw, err := hj.Hijack()
			if err != nil {
				return
			}

			// Write HTTP 101 Switching Protocols response manually.
			_, _ = bufrw.WriteString("HTTP/1.1 101 UPGRADED\r\n")                                   //nolint:errcheck
			_, _ = bufrw.WriteString("Content-Type: application/vnd.docker.multiplexed-stream\r\n") //nolint:errcheck
			_, _ = bufrw.WriteString("\r\n")                                                        //nolint:errcheck

			// Write mock stdout in Docker multiplexed stream format.
			// Header: [stream_type(1B), 0, 0, 0, size(4B big-endian)]
			payload := []byte("output\n")
			var hdr [8]byte
			hdr[0] = 1                                                // stdout
			binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload))) //nolint:gosec
			_, _ = bufrw.Write(hdr[:])                                //nolint:errcheck
			_, _ = bufrw.Write(payload)                               //nolint:errcheck
			_ = bufrw.Flush()                                         //nolint:errcheck

			// Half-close the write side so the client gets EOF instead of
			// a connection reset when stdcopy.StdCopy reads to completion.
			if tc, ok2 := conn.(*net.TCPConn); ok2 {
				_ = tc.CloseWrite() //nolint:errcheck
			} else {
				_ = conn.Close() //nolint:errcheck
			}
		},
		"GET /exec/exec1/json": func(w http.ResponseWriter, _ *http.Request) {
			exitCode := 0
			writeJSON(w, http.StatusOK, containertypes.ExecInspectResponse{
				ExitCode: &exitCode,
			})
		},
	})

	result, err := e.Exec(t.Context(), "abc123", ExecOpts{
		Cmd:          []string{"echo", "output"},
		AttachStdout: true,
		AttachStderr: true,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	require.NotNil(t, result.Stdout)
	out, readErr := io.ReadAll(result.Stdout)
	require.NoError(t, readErr)
	assert.Equal(t, "output\n", string(out))
}

// TestDockerEngineWaitContainer verifies that WaitContainer returns the exit
// status from the mock daemon via the wait endpoint.
func TestDockerEngineWaitContainer(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"POST /containers/abc123/wait": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, containertypes.WaitResponse{StatusCode: 0})
		},
	})

	ch, err := e.WaitContainer(t.Context(), "abc123", WaitContainerOpts{})
	require.NoError(t, err)
	select {
	case res := <-ch:
		assert.Equal(t, 0, res.StatusCode)
		assert.Empty(t, res.Error)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for WaitContainer result")
	}
}

// TestDockerEngineEvents verifies that Events returns a channel that yields
// events from the mock daemon's event stream.
func TestDockerEngineEvents(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"GET /events": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(eventtypes.Message{ //nolint:errcheck
				Type:   eventtypes.ContainerEventType,
				Action: eventtypes.ActionStart,
				Actor:  eventtypes.Actor{ID: "abc123"},
			})
			// Closing the response ends the event stream.
		},
	})

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	ch, err := e.Events(ctx)
	require.NoError(t, err)

	select {
	case ev, ok := <-ch:
		if !ok {
			// Channel closed before receiving event; may happen if EOF came first.
			return
		}
		assert.Equal(t, string(eventtypes.ContainerEventType), ev.Type)
		assert.Equal(t, string(eventtypes.ActionStart), ev.Action)
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}
}

// TestDockerEngineListImages verifies that ListImages decodes the image list.
func TestDockerEngineListImages(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"GET /images/json": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, []imagetypes.Summary{
				{ID: "sha256:abc123", RepoTags: []string{"nginx:latest"}, Size: 50_000_000},
			})
		},
	})

	images, err := e.ListImages(t.Context(), ListImagesOpts{})
	require.NoError(t, err)
	require.Len(t, images, 1)
	assert.Equal(t, "sha256:abc123", images[0].ID)
	assert.Equal(t, []string{"nginx:latest"}, images[0].Tags)
}

// TestDockerEngineRemoveImage verifies that RemoveImage deletes the image.
func TestDockerEngineRemoveImage(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"DELETE /images/nginx:latest": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, []imagetypes.DeleteResponse{
				{Deleted: "sha256:abc123"},
			})
		},
	})

	require.NoError(t, e.RemoveImage(t.Context(), "nginx:latest", RemoveImageOpts{}))
}

// TestDockerEngineTagImage verifies that TagImage posts to /images/{src}/tag.
func TestDockerEngineTagImage(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"POST /images/nginx:latest/tag": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
		},
	})

	require.NoError(t, e.TagImage(t.Context(), "nginx:latest", "myrepo/nginx:v1"))
}

// TestDockerEngineCreateNetwork verifies that CreateNetwork posts to
// /networks/create and returns the network ID.
func TestDockerEngineCreateNetwork(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"POST /networks/create": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusCreated, networktypes.CreateResponse{ID: "net123"})
		},
	})

	id, err := e.CreateNetwork(t.Context(), "mynet", CreateNetworkOpts{Driver: "bridge"})
	require.NoError(t, err)
	assert.Equal(t, NetworkID("net123"), id)
}

// TestDockerEngineListNetworks verifies that ListNetworks decodes the list.
func TestDockerEngineListNetworks(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"GET /networks": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, []networktypes.Summary{
				{Network: networktypes.Network{ID: "net123", Name: "mynet", Driver: "bridge"}},
			})
		},
	})

	nets, err := e.ListNetworks(t.Context(), ListNetworksOpts{})
	require.NoError(t, err)
	require.Len(t, nets, 1)
	assert.Equal(t, NetworkID("net123"), nets[0].ID)
	assert.Equal(t, "mynet", nets[0].Name)
	assert.Equal(t, "bridge", nets[0].Driver)
}

// TestDockerEngineRemoveNetwork verifies that RemoveNetwork deletes the network.
func TestDockerEngineRemoveNetwork(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"DELETE /networks/net123": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
	})

	require.NoError(t, e.RemoveNetwork(t.Context(), "net123", RemoveNetworkOpts{}))
}

// TestDockerEngineConnectContainer verifies that ConnectContainer posts to
// /networks/{net}/connect.
func TestDockerEngineConnectContainer(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"POST /networks/net123/connect": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
	})

	require.NoError(t, e.ConnectContainer(t.Context(), "net123", "abc123", ConnectOpts{Aliases: []string{"alias"}}))
}

// TestDockerEngineDisconnectContainer verifies that DisconnectContainer posts
// to /networks/{net}/disconnect, with and without the Force option.
func TestDockerEngineDisconnectContainer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		network NetworkID
		opts    DisconnectOpts
	}{
		{
			name:    "default opts",
			network: "net123",
			opts:    DisconnectOpts{},
		},
		{
			name:    "force flag",
			network: "net1",
			opts:    DisconnectOpts{Force: true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
				"POST /networks/" + string(tc.network) + "/disconnect": func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				},
			})
			require.NoError(t, e.DisconnectContainer(t.Context(), tc.network, "abc123", tc.opts))
		})
	}
}

// TestDockerEngineCreateVolume verifies that CreateVolume posts to /volumes/create
// and returns the volume ID.
func TestDockerEngineCreateVolume(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"POST /volumes/create": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusCreated, volumetypes.Volume{Name: "myvol", Driver: "local"})
		},
	})

	id, err := e.CreateVolume(t.Context(), "myvol", CreateVolumeOpts{Driver: "local"})
	require.NoError(t, err)
	assert.Equal(t, VolumeID("myvol"), id)
}

// TestDockerEngineListVolumes verifies that ListVolumes decodes the volume list.
func TestDockerEngineListVolumes(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"GET /volumes": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, volumetypes.ListResponse{
				Volumes: []volumetypes.Volume{
					{Name: "myvol", Driver: "local", Mountpoint: "/var/lib/docker/volumes/myvol"},
				},
			})
		},
	})

	vols, err := e.ListVolumes(t.Context(), ListVolumesOpts{})
	require.NoError(t, err)
	require.Len(t, vols, 1)
	assert.Equal(t, VolumeID("myvol"), vols[0].ID)
	assert.Equal(t, "local", vols[0].Driver)
}

// TestDockerEngineRemoveVolume verifies that RemoveVolume deletes the volume.
func TestDockerEngineRemoveVolume(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"DELETE /volumes/myvol": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	})

	require.NoError(t, e.RemoveVolume(t.Context(), "myvol", RemoveVolumeOpts{}))
}

// TestDockerEngineCopyToContainer verifies that CopyToContainer issues a PUT
// to /containers/{id}/archive with the tar content.
func TestDockerEngineCopyToContainer(t *testing.T) {
	t.Parallel()

	var received []byte
	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"PUT /containers/abc123/archive": func(w http.ResponseWriter, r *http.Request) {
			received, _ = io.ReadAll(r.Body) //nolint:errcheck
			w.WriteHeader(http.StatusOK)
		},
	})

	// Build a small tar archive to send.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "hello.txt",
		Size: 5,
		Mode: 0o644,
	}))
	_, err := tw.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	err = e.CopyToContainer(t.Context(), "abc123", CopyToContainerOpts{
		DestPath: "/tmp",
		Content:  bytes.NewReader(buf.Bytes()),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, received)
}

// TestDockerEngineCopyFromContainer verifies that CopyFromContainer issues a
// GET to /containers/{id}/archive and returns the response body.
func TestDockerEngineCopyFromContainer(t *testing.T) {
	t.Parallel()

	// Build a minimal tar archive to return.
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "hello.txt", Size: 5, Mode: 0o644}))
	_, err := tw.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	// CopyFromContainer requires an X-Docker-Container-Path-Stat header
	// containing a base64-encoded JSON PathStat.
	pathStatJSON, err := json.Marshal(map[string]any{
		"name": "hello.txt",
		"size": 5,
		"mode": 0o644,
	})
	require.NoError(t, err)
	pathStatHeader := base64.StdEncoding.EncodeToString(pathStatJSON)

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"GET /containers/abc123/archive": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/x-tar")
			w.Header().Set("X-Docker-Container-Path-Stat", pathStatHeader)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(tarBuf.Bytes()) //nolint:errcheck
		},
	})

	rc, err := e.CopyFromContainer(t.Context(), "abc123", CopyFromContainerOpts{SrcPath: "/tmp/hello.txt"})
	require.NoError(t, err)
	defer func() { assert.NoError(t, rc.Close()) }()

	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.NotEmpty(t, data)
}

// TestDockerEngineErrorPaths verifies that each engine method wraps a
// non-2xx daemon response as a non-nil error. Adding a new method's error
// path only requires a new row in the table.
func TestDockerEngineErrorPaths(t *testing.T) {
	t.Parallel()

	notFound := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
	}
	serverErr := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "server error"})
	}

	tests := []struct {
		name     string
		handlers map[string]http.HandlerFunc
		run      func(t *testing.T, e *dockerEngine) error
	}{
		{
			name:     "PullImage",
			handlers: map[string]http.HandlerFunc{"POST /images/create": notFound},
			run: func(t *testing.T, e *dockerEngine) error {
				t.Helper()
				return e.PullImage(t.Context(), "no-such-image:latest", PullImageOpts{})
			},
		},
		{
			name:     "StartContainer",
			handlers: map[string]http.HandlerFunc{"POST /containers/c/start": notFound},
			run: func(t *testing.T, e *dockerEngine) error {
				t.Helper()
				return e.StartContainer(t.Context(), "c")
			},
		},
		{
			name:     "RemoveContainer",
			handlers: map[string]http.HandlerFunc{"DELETE /containers/c": notFound},
			run: func(t *testing.T, e *dockerEngine) error {
				t.Helper()
				return e.RemoveContainer(t.Context(), "c", RemoveContainerOpts{})
			},
		},
		{
			name:     "Stats",
			handlers: map[string]http.HandlerFunc{"GET /containers/c/stats": notFound},
			run: func(t *testing.T, e *dockerEngine) error {
				t.Helper()
				_, err := e.Stats(t.Context(), "c", StatsOpts{})

				return err
			},
		},
		{
			name:     "ExecCreate",
			handlers: map[string]http.HandlerFunc{"POST /containers/c/exec": notFound},
			run: func(t *testing.T, e *dockerEngine) error {
				t.Helper()
				_, err := e.Exec(t.Context(), "c", ExecOpts{Cmd: []string{"ls"}})

				return err
			},
		},
		{
			name:     "RemoveImage",
			handlers: map[string]http.HandlerFunc{"DELETE /images/img": notFound},
			run: func(t *testing.T, e *dockerEngine) error {
				t.Helper()
				return e.RemoveImage(t.Context(), "img", RemoveImageOpts{})
			},
		},
		{
			name:     "TagImage",
			handlers: map[string]http.HandlerFunc{"POST /images/img/tag": notFound},
			run: func(t *testing.T, e *dockerEngine) error {
				t.Helper()
				return e.TagImage(t.Context(), "img", "dst:latest")
			},
		},
		{
			name:     "CreateNetwork",
			handlers: map[string]http.HandlerFunc{"POST /networks/create": serverErr},
			run: func(t *testing.T, e *dockerEngine) error {
				t.Helper()
				_, err := e.CreateNetwork(t.Context(), "net", CreateNetworkOpts{})

				return err
			},
		},
		{
			name:     "RemoveNetwork",
			handlers: map[string]http.HandlerFunc{"DELETE /networks/net": notFound},
			run: func(t *testing.T, e *dockerEngine) error {
				t.Helper()
				return e.RemoveNetwork(t.Context(), "net", RemoveNetworkOpts{})
			},
		},
		{
			name:     "CreateVolume",
			handlers: map[string]http.HandlerFunc{"POST /volumes/create": serverErr},
			run: func(t *testing.T, e *dockerEngine) error {
				t.Helper()
				_, err := e.CreateVolume(t.Context(), "vol", CreateVolumeOpts{})

				return err
			},
		},
		{
			name:     "RemoveVolume",
			handlers: map[string]http.HandlerFunc{"DELETE /volumes/vol": notFound},
			run: func(t *testing.T, e *dockerEngine) error {
				t.Helper()
				return e.RemoveVolume(t.Context(), "vol", RemoveVolumeOpts{})
			},
		},
		{
			name:     "CopyToContainer",
			handlers: map[string]http.HandlerFunc{"PUT /containers/c/archive": notFound},
			run: func(t *testing.T, e *dockerEngine) error {
				t.Helper()
				var buf bytes.Buffer

				return e.CopyToContainer(t.Context(), "c", CopyToContainerOpts{DestPath: "/tmp", Content: &buf})
			},
		},
		{
			name:     "CopyFromContainer",
			handlers: map[string]http.HandlerFunc{"GET /containers/c/archive": notFound},
			run: func(t *testing.T, e *dockerEngine) error {
				t.Helper()
				_, err := e.CopyFromContainer(t.Context(), "c", CopyFromContainerOpts{SrcPath: "/tmp"})

				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newMockDockerDaemon(t, tc.handlers)
			require.Error(t, tc.run(t, e))
		})
	}
}

// TestDockerEngineEventsClosedStream verifies Events returns a closed channel
// when the server closes the event stream immediately.
func TestDockerEngineEventsClosedStream(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"GET /events": func(w http.ResponseWriter, _ *http.Request) {
			// Respond with no body — immediate EOF.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
		},
	})

	ch, err := e.Events(t.Context())
	require.NoError(t, err)

	select {
	case _, ok := <-ch:
		assert.Falsef(t, ok, "channel should be closed on empty stream")
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Events channel to close")
	}
}

// TestDockerEngineExecCreateError verifies that Exec returns an error when
// ExecCreate fails.
func TestDockerEngineExecCreateError(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"POST /containers/nocontainer/exec": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container"})
		},
	})

	_, err := e.Exec(t.Context(), "nocontainer", ExecOpts{Cmd: []string{"ls"}})
	require.Error(t, err)
}

// TestDockerEngineEventsError verifies that Events closes the channel after
// the daemon returns an error response. This exercises the case err := <-raw.Err
// branch with a non-nil error.
func TestDockerEngineEventsError(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"GET /events": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"message": "events stream unavailable",
			})
		},
	})

	ch, err := e.Events(t.Context())
	require.NoError(t, err)

	// The error from the server is propagated via raw.Err; the goroutine
	// logs it and closes out. We should see the channel close.
	select {
	case _, ok := <-ch:
		assert.Falsef(t, ok, "channel should close after stream error")
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Events channel to close on error")
	}
}

// TestDockerEngineCredentials verifies that Credentials dispatches to the
// appropriate credential chain for each engine kind. It uses environment-only
// credentials so no binary helpers need to be on PATH.
func TestDockerEngineCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind EngineKind
	}{
		{"docker", Docker},
		{"podman", Podman},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newMockDockerDaemon(t, nil)
			e.kind = tc.kind
			// Credentials reads the file system / env; we just verify it
			// returns without an unexpected error. An empty map is valid
			// when no credential store is configured in the test environment.
			_, err := e.Credentials(t.Context())
			assert.NoError(t, err)
		})
	}
}

// TestDockerEnginePullImageWithPlatform verifies PullImage sends the platform
// constraint when o.Platform is set.
func TestDockerEnginePullImageWithPlatform(t *testing.T) {
	t.Parallel()

	var requestedRef string

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"POST /images/create": func(w http.ResponseWriter, r *http.Request) {
			requestedRef = r.URL.Query().Get("fromImage")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"Pull complete"}`)) //nolint:errcheck
		},
	})

	err := e.PullImage(t.Context(), "nginx:latest", PullImageOpts{Platform: "linux/amd64"})
	require.NoError(t, err)
	assert.Contains(t, requestedRef, "nginx")
}

// TestDockerEngineCreateContainerNetworkConnectError verifies that
// CreateContainer returns an error when connecting a second network fails.
func TestDockerEngineCreateContainerNetworkConnectError(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"POST /containers/create": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusCreated, containertypes.CreateResponse{ID: "errnet1"})
		},
		"POST /networks/net2/connect": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "network error"})
		},
	})

	spec := ContainerSpec{
		Image: "nginx:latest",
		Networks: []NetworkAttachment{
			{Name: "net1"},
			{Name: "net2"},
		},
	}
	_, err := e.CreateContainer(t.Context(), spec)
	require.Error(t, err)
}

// TestDockerEngineCreateContainerMultipleNetworks verifies that CreateContainer
// calls NetworkConnect for each extra network beyond the first.
func TestDockerEngineCreateContainerMultipleNetworks(t *testing.T) {
	t.Parallel()

	networkConnectCalls := 0

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"POST /containers/create": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusCreated, containertypes.CreateResponse{ID: "multi1"})
		},
		"POST /networks/net2/connect": func(w http.ResponseWriter, _ *http.Request) {
			networkConnectCalls++
			w.WriteHeader(http.StatusOK)
		},
		"POST /networks/net3/connect": func(w http.ResponseWriter, _ *http.Request) {
			networkConnectCalls++
			w.WriteHeader(http.StatusOK)
		},
	})

	spec := ContainerSpec{
		Image: "nginx:latest",
		Networks: []NetworkAttachment{
			{Name: "net1"},
			{Name: "net2"},
			{Name: "net3"},
		},
	}
	id, err := e.CreateContainer(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, ContainerID("multi1"), id)
	assert.Equalf(t, 2, networkConnectCalls, "should call NetworkConnect for each extra network")
}

// TestDockerEngineWaitContainerConditions verifies all WaitCondition variants
// and the daemon-error-message path in a single table.
func TestDockerEngineWaitContainerConditions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		containerID string
		opts        WaitContainerOpts
		response    containertypes.WaitResponse
		wantCode    int
		wantErrMsg  string
	}{
		{
			name:        "WaitConditionNextExit",
			containerID: "c1",
			opts:        WaitContainerOpts{Condition: WaitConditionNextExit},
			response:    containertypes.WaitResponse{StatusCode: 42},
			wantCode:    42,
		},
		{
			name:        "WaitConditionRemoved",
			containerID: "c2",
			opts:        WaitContainerOpts{Condition: WaitConditionRemoved},
			response:    containertypes.WaitResponse{StatusCode: 0},
			wantCode:    0,
		},
		{
			name:        "daemon error message forwarded",
			containerID: "c3",
			opts:        WaitContainerOpts{},
			response: containertypes.WaitResponse{
				StatusCode: 1,
				Error:      &containertypes.WaitExitError{Message: "container failed to exit cleanly"},
			},
			wantCode:   1,
			wantErrMsg: "container failed to exit cleanly",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
				"POST /containers/" + tc.containerID + "/wait": func(w http.ResponseWriter, _ *http.Request) {
					writeJSON(w, http.StatusOK, tc.response)
				},
			})
			ch, err := e.WaitContainer(t.Context(), ContainerID(tc.containerID), tc.opts)
			require.NoError(t, err)
			select {
			case res := <-ch:
				assert.Equal(t, tc.wantCode, res.StatusCode)
				assert.Equal(t, tc.wantErrMsg, res.Error)
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for WaitContainer result")
			}
		})
	}
}

// TestDockerEnginePingError verifies Ping wraps the error from a failed ping.
func TestDockerEnginePingError(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"HEAD /_ping": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
		"GET /_ping": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
	})

	err := e.Ping(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker: ping")
}

// TestDockerEngineCreateContainerWithInitAndPorts verifies CreateContainer
// sets the Init flag and port bindings correctly.
func TestDockerEngineCreateContainerWithInitAndPorts(t *testing.T) {
	t.Parallel()

	e := newMockDockerDaemon(t, map[string]http.HandlerFunc{
		"POST /containers/create": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusCreated, containertypes.CreateResponse{ID: "init1"})
		},
	})

	spec := ContainerSpec{
		Image: "nginx:latest",
		Init:  true,
		Ports: []Port{
			{Container: 80, Protocol: "tcp"},
		},
	}
	id, err := e.CreateContainer(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, ContainerID("init1"), id)
}
