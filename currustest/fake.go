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

// Package currustest provides an in-memory fake [currus.Engine] for use in
// tests. [Fake] implements currus.Engine plus all capability interfaces
// ([currus.Logger], [currus.Execer], [currus.Inspector], [currus.Stater],
// [currus.Waiter], [currus.Eventer]) so that callers can test code paths that
// branch on optional features without running a real container daemon.
//
// The fake is also the primary target of the conformance suite in
// [gopherly.dev/currus/conformance]; keeping the fake conformant prevents it
// from lying about the contract that real drivers must honor.
//
// Usage:
//
//	eng := currustest.New()
//	// drive the same code path against the fake
//	id, err := eng.CreateContainer(ctx, currus.ContainerSpec{Image: "nginx:latest"})
package currustest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"gopherly.dev/currus"
)

// Fake is the in-memory fake engine. It is safe for concurrent use.
type Fake struct {
	mu         sync.RWMutex
	containers map[currus.ContainerID]*fakeContainer
	images     map[string]bool
	networks   map[currus.NetworkID]currus.Network
	// netMembers maps network ID to the set of container IDs currently attached.
	netMembers map[currus.NetworkID]map[currus.ContainerID]struct{}
	volumes    map[currus.VolumeID]currus.Volume
	counter    atomic.Uint64
}

type fakeContainer struct {
	spec  currus.ContainerSpec
	state string // "created" | "running" | "exited"
	logs  string
}

// Compile-time assertions.
var (
	_ currus.Engine           = (*Fake)(nil)
	_ currus.Logger           = (*Fake)(nil)
	_ currus.Execer           = (*Fake)(nil)
	_ currus.Inspector        = (*Fake)(nil)
	_ currus.Stater           = (*Fake)(nil)
	_ currus.Waiter           = (*Fake)(nil)
	_ currus.Eventer          = (*Fake)(nil)
	_ currus.Imager           = (*Fake)(nil)
	_ currus.Networker        = (*Fake)(nil)
	_ currus.Volumer          = (*Fake)(nil)
	_ currus.Copier           = (*Fake)(nil)
	_ currus.EndpointReporter = (*Fake)(nil)
)

// New returns a ready-to-use in-memory fake engine.
func New() *Fake {
	return &Fake{
		containers: make(map[currus.ContainerID]*fakeContainer),
		images:     make(map[string]bool),
		networks:   make(map[currus.NetworkID]currus.Network),
		netMembers: make(map[currus.NetworkID]map[currus.ContainerID]struct{}),
		volumes:    make(map[currus.VolumeID]currus.Volume),
	}
}

// Kind returns the fake's engine kind.
func (e *Fake) Kind() currus.EngineKind {
	return currus.EngineKind("fake")
}

// Capabilities returns zero-value Caps (fake supports nothing non-trivial).
func (e *Fake) Capabilities() currus.Caps {
	return currus.Caps{}
}

// Ping always succeeds.
func (e *Fake) Ping(_ context.Context) error {
	return nil
}

// Close is a no-op.
func (e *Fake) Close() error {
	return nil
}

// PullImage marks the image as available in the fake's store.
func (e *Fake) PullImage(_ context.Context, ref string, _ currus.PullImageOpts) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.images[ref] = true

	return nil
}

// CreateContainer creates an in-memory container. It does NOT require the
// image to have been pulled first; the fake is permissive to ease test setup.
// Any networks listed in spec.Networks are recorded as memberships immediately.
func (e *Fake) CreateContainer(_ context.Context, spec currus.ContainerSpec) (currus.ContainerID, error) {
	if err := spec.Validate(); err != nil {
		return "", fmt.Errorf("currustest: create container: %w", err)
	}
	id := currus.ContainerID(fmt.Sprintf("fake-%d", e.counter.Add(1)))
	e.mu.Lock()
	defer e.mu.Unlock()
	e.containers[id] = &fakeContainer{spec: spec, state: "created"}
	for _, n := range spec.Networks {
		netID := currus.NetworkID(n.Name)
		if _, ok := e.netMembers[netID]; !ok {
			e.netMembers[netID] = make(map[currus.ContainerID]struct{})
		}
		e.netMembers[netID][id] = struct{}{}
	}

	return id, nil
}

// StartContainer transitions a container from "created" or "exited" to "running".
func (e *Fake) StartContainer(_ context.Context, id currus.ContainerID) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.containers[id]
	if !ok {
		return fmt.Errorf("currustest: start %s: %w", id, currus.ErrNotFound)
	}
	if c.state == "running" {
		return fmt.Errorf("currustest: start %s: %w: already running", id, currus.ErrConflict)
	}
	c.state = "running"
	c.logs = fmt.Sprintf("[%s] container started\n", id)

	return nil
}

// StopContainer transitions a running container to "exited".
func (e *Fake) StopContainer(_ context.Context, id currus.ContainerID, _ currus.StopContainerOpts) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.containers[id]
	if !ok {
		return fmt.Errorf("currustest: stop %s: %w", id, currus.ErrNotFound)
	}
	if c.state != "running" {
		return fmt.Errorf("currustest: stop %s: %w: not running", id, currus.ErrConflict)
	}
	c.state = "exited"

	return nil
}

// RemoveContainer deletes a container and removes it from any network memberships.
func (e *Fake) RemoveContainer(_ context.Context, id currus.ContainerID, o currus.RemoveContainerOpts) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.containers[id]
	if !ok {
		return fmt.Errorf("currustest: remove %s: %w", id, currus.ErrNotFound)
	}
	if c.state == "running" && !o.Force {
		return fmt.Errorf("currustest: remove %s: %w: container is running", id, currus.ErrConflict)
	}
	delete(e.containers, id)
	for netID, members := range e.netMembers {
		delete(members, id)
		if len(members) == 0 {
			delete(e.netMembers, netID)
		}
	}

	return nil
}

// ListContainers returns the containers tracked by the fake.
func (e *Fake) ListContainers(_ context.Context, o currus.ListContainersOpts) ([]currus.Container, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]currus.Container, 0, len(e.containers))
	for id, c := range e.containers {
		if !o.All && c.state != "running" {
			continue
		}
		out = append(out, currus.Container{
			ID:     id,
			Name:   c.spec.Name,
			Image:  c.spec.Image,
			State:  c.state,
			Labels: c.spec.Labels,
		})
	}

	return out, nil
}

// ContainerLogs implements currus.Logger.
// Returns the fake log string written when the container was started.
func (e *Fake) ContainerLogs(_ context.Context, id currus.ContainerID, _ currus.ContainerLogsOpts) (io.ReadCloser, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, ok := e.containers[id]
	if !ok {
		return nil, fmt.Errorf("currustest: logs %s: %w", id, currus.ErrNotFound)
	}

	return io.NopCloser(strings.NewReader(c.logs)), nil
}

// Exec implements currus.Execer.
// Returns a zero-exit result containing the joined cmd as stdout.
func (e *Fake) Exec(_ context.Context, id currus.ContainerID, o currus.ExecOpts) (currus.ExecResult, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if _, ok := e.containers[id]; !ok {
		return currus.ExecResult{}, fmt.Errorf("currustest: exec %s: %w", id, currus.ErrNotFound)
	}
	output := strings.Join(o.Cmd, " ") + "\n"
	result := currus.ExecResult{ExitCode: 0}
	if o.AttachStdout {
		result.Stdout = bytes.NewBufferString(output)
	}
	if o.AttachStderr {
		result.Stderr = bytes.NewBufferString("")
	}

	return result, nil
}

// Inspect implements currus.Inspector.
func (e *Fake) Inspect(_ context.Context, id currus.ContainerID) (currus.ContainerInfo, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, ok := e.containers[id]
	if !ok {
		return currus.ContainerInfo{}, fmt.Errorf("currustest: inspect %s: %w", id, currus.ErrNotFound)
	}

	return currus.ContainerInfo{
		ID:     id,
		Name:   c.spec.Name,
		Image:  c.spec.Image,
		Labels: c.spec.Labels,
		State: currus.ContainerState{
			Running: c.state == "running",
		},
		Command:    append(c.spec.Command, c.spec.Args...),
		Env:        c.spec.Env,
		WorkingDir: c.spec.WorkingDir,
		Mounts:     c.spec.Mounts,
	}, nil
}

// Stats implements currus.Stater. Returns zeroed stats for the fake.
func (e *Fake) Stats(_ context.Context, id currus.ContainerID, _ currus.StatsOpts) (currus.ContainerStats, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if _, ok := e.containers[id]; !ok {
		return currus.ContainerStats{}, fmt.Errorf("currustest: stats %s: %w", id, currus.ErrNotFound)
	}

	return currus.ContainerStats{}, nil
}

// WaitContainer implements currus.Waiter.
// The fake returns a channel that immediately yields StatusCode 0 for stopped
// containers, and blocks until Stop is called for running ones.
func (e *Fake) WaitContainer(_ context.Context, id currus.ContainerID, _ currus.WaitContainerOpts) (<-chan currus.WaitResult, error) {
	e.mu.RLock()
	_, ok := e.containers[id]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("currustest: wait %s: %w", id, currus.ErrNotFound)
	}
	out := make(chan currus.WaitResult, 1)
	// The fake does not support real blocking wait; signal exit 0 immediately.
	out <- currus.WaitResult{StatusCode: 0}
	close(out)

	return out, nil
}

// Events implements currus.Eventer.
// The fake returns a channel that is closed immediately (no background events).
func (e *Fake) Events(_ context.Context) (<-chan currus.Event, error) {
	out := make(chan currus.Event)
	close(out)

	return out, nil
}

// SetLogs injects log content for a container, for use in tests.
// If id is unknown the call is a no-op.
func (e *Fake) SetLogs(id currus.ContainerID, logs string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.containers[id]; ok {
		c.logs = logs
	}
}

// ListImages implements currus.Imager.
func (e *Fake) ListImages(_ context.Context, _ currus.ListImagesOpts) ([]currus.Image, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]currus.Image, 0, len(e.images))
	for ref := range e.images {
		out = append(out, currus.Image{ID: ref, Tags: []string{ref}})
	}

	return out, nil
}

// RemoveImage implements currus.Imager.
func (e *Fake) RemoveImage(_ context.Context, ref string, _ currus.RemoveImageOpts) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.images[ref] {
		return fmt.Errorf("currustest: remove image %s: %w", ref, currus.ErrNotFound)
	}
	delete(e.images, ref)

	return nil
}

// TagImage implements currus.Imager.
func (e *Fake) TagImage(_ context.Context, src, dst string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.images[src] {
		return fmt.Errorf("currustest: tag image %s: %w", src, currus.ErrNotFound)
	}
	e.images[dst] = true

	return nil
}

// CreateNetwork implements currus.Networker.
func (e *Fake) CreateNetwork(_ context.Context, name string, o currus.CreateNetworkOpts) (currus.NetworkID, error) {
	id := currus.NetworkID("fake-net-" + name)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.networks[id] = currus.Network{ID: id, Name: name, Driver: o.Driver}

	return id, nil
}

// ListNetworks implements currus.Networker.
func (e *Fake) ListNetworks(_ context.Context, _ currus.ListNetworksOpts) ([]currus.Network, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]currus.Network, 0, len(e.networks))
	for _, n := range e.networks {
		out = append(out, n)
	}

	return out, nil
}

// RemoveNetwork implements currus.Networker.
func (e *Fake) RemoveNetwork(_ context.Context, id currus.NetworkID, _ currus.RemoveNetworkOpts) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.networks[id]; !ok {
		return fmt.Errorf("currustest: remove network %s: %w", id, currus.ErrNotFound)
	}
	delete(e.networks, id)
	delete(e.netMembers, id)

	return nil
}

// ConnectContainer implements currus.Networker.
func (e *Fake) ConnectContainer(_ context.Context, net currus.NetworkID, id currus.ContainerID, _ currus.ConnectOpts) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.containers[id]; !ok {
		return fmt.Errorf("currustest: connect %s: %w", id, currus.ErrNotFound)
	}
	if _, ok := e.netMembers[net]; !ok {
		e.netMembers[net] = make(map[currus.ContainerID]struct{})
	}
	e.netMembers[net][id] = struct{}{}

	return nil
}

// DisconnectContainer implements currus.Networker.
func (e *Fake) DisconnectContainer(_ context.Context, net currus.NetworkID, id currus.ContainerID, _ currus.DisconnectOpts) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.containers[id]; !ok {
		return fmt.Errorf("currustest: disconnect %s: %w", id, currus.ErrNotFound)
	}
	if members, ok := e.netMembers[net]; ok {
		delete(members, id)
		if len(members) == 0 {
			delete(e.netMembers, net)
		}
	}

	return nil
}

// Endpoint implements currus.EndpointReporter.
// Returns a synthetic endpoint suitable for tests.
func (e *Fake) Endpoint() currus.Endpoint {
	return currus.Endpoint{Host: "unix:///var/run/fake.sock"}
}

// NetworkMembers returns the set of container IDs currently attached to net.
// Intended for use in tests that need to assert network membership.
func (e *Fake) NetworkMembers(net currus.NetworkID) []currus.ContainerID {
	e.mu.RLock()
	defer e.mu.RUnlock()
	members := e.netMembers[net]
	out := make([]currus.ContainerID, 0, len(members))
	for id := range members {
		out = append(out, id)
	}

	return out
}

// CreateVolume implements currus.Volumer.
func (e *Fake) CreateVolume(_ context.Context, name string, o currus.CreateVolumeOpts) (currus.VolumeID, error) {
	id := currus.VolumeID("fake-vol-" + name)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.volumes[id] = currus.Volume{ID: id, Driver: o.Driver, Mountpoint: "/tmp/" + name}

	return id, nil
}

// ListVolumes implements currus.Volumer.
func (e *Fake) ListVolumes(_ context.Context, _ currus.ListVolumesOpts) ([]currus.Volume, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]currus.Volume, 0, len(e.volumes))
	for _, v := range e.volumes {
		out = append(out, v)
	}

	return out, nil
}

// RemoveVolume implements currus.Volumer.
func (e *Fake) RemoveVolume(_ context.Context, id currus.VolumeID, _ currus.RemoveVolumeOpts) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.volumes[id]; !ok {
		return fmt.Errorf("currustest: remove volume %s: %w", id, currus.ErrNotFound)
	}
	delete(e.volumes, id)

	return nil
}

// CopyToContainer implements currus.Copier. No-op in the fake.
func (e *Fake) CopyToContainer(_ context.Context, id currus.ContainerID, _ currus.CopyToContainerOpts) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if _, ok := e.containers[id]; !ok {
		return fmt.Errorf("currustest: copy to %s: %w", id, currus.ErrNotFound)
	}

	return nil
}

// CopyFromContainer implements currus.Copier. Returns an empty TAR archive.
func (e *Fake) CopyFromContainer(_ context.Context, id currus.ContainerID, _ currus.CopyFromContainerOpts) (io.ReadCloser, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if _, ok := e.containers[id]; !ok {
		return nil, fmt.Errorf("currustest: copy from %s: %w", id, currus.ErrNotFound)
	}

	return io.NopCloser(bytes.NewReader(nil)), nil
}

// ContainerState returns the state of the container, or "" if unknown.
func (e *Fake) ContainerState(id currus.ContainerID) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if c, ok := e.containers[id]; ok {
		return c.state
	}

	return ""
}
