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
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/platforms"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"go.opentelemetry.io/otel/trace"

	cerrdefs "github.com/containerd/errdefs"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Well-known default socket paths used by autodetection.
const (
	defaultDockerSocket        = "/var/run/docker.sock"
	defaultPodmanRootfulSocket = "/run/podman/podman.sock"
)

// dockerDriverKind identifies whether the client is pointed at a Docker or
// Podman socket. Used only for reporting the EngineKind back to callers.
type dockerDriverKind int

const (
	dockerKindDocker dockerDriverKind = iota
	dockerKindPodman
)

// dockerConfig holds the parameters needed to build a dockerEngine.
type dockerConfig struct {
	// Host is the Docker socket URI (e.g. "unix:///var/run/docker.sock").
	Host string

	// Kind distinguishes Docker from Podman for EngineKind reporting.
	Kind dockerDriverKind

	// TLS is the TLS configuration for tcp:// connections.
	TLS *tls.Config

	// Logger is the slog logger. Nil is replaced with slog.Default().
	Logger *slog.Logger

	// Tracer is the OTel TracerProvider. Nil means no tracing.
	Tracer trace.TracerProvider
}

// dockerEngine is the Docker-API driver serving both Docker and Podman.
type dockerEngine struct {
	cli    *client.Client
	kind   EngineKind
	caps   Caps
	host   string // resolved URI, e.g. "unix:///var/run/docker.sock"
	logger *slog.Logger
	tracer trace.TracerProvider
}

// Compile-time assertions.
var (
	_ Engine           = (*dockerEngine)(nil)
	_ Logger           = (*dockerEngine)(nil)
	_ Execer           = (*dockerEngine)(nil)
	_ Inspector        = (*dockerEngine)(nil)
	_ Stater           = (*dockerEngine)(nil)
	_ Waiter           = (*dockerEngine)(nil)
	_ Eventer          = (*dockerEngine)(nil)
	_ Imager           = (*dockerEngine)(nil)
	_ Networker        = (*dockerEngine)(nil)
	_ Volumer          = (*dockerEngine)(nil)
	_ Copier           = (*dockerEngine)(nil)
	_ EndpointReporter = (*dockerEngine)(nil)
)

// newDockerEngine creates a dockerEngine using the given dockerConfig.
func newDockerEngine(cfg dockerConfig) (*dockerEngine, error) {
	var opts []client.Opt
	if cfg.Host != "" {
		opts = append(opts, client.WithHost(cfg.Host))
	}
	if cfg.TLS != nil {
		opts = append(opts, client.WithHTTPClient(&http.Client{
			Transport: &http.Transport{TLSClientConfig: cfg.TLS},
		}))
	}
	if cfg.Tracer != nil {
		opts = append(opts, client.WithTraceProvider(cfg.Tracer))
	}

	cli, err := client.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker: create client: %w", err)
	}

	lg := cfg.Logger
	if lg == nil {
		lg = slog.Default()
	}

	kind := Docker
	if cfg.Kind == dockerKindPodman {
		kind = Podman
	}

	host := cfg.Host
	if host == "" {
		host = "unix://" + defaultDockerSocket
	}

	return &dockerEngine{
		cli:    cli,
		kind:   kind,
		caps:   buildDockerCaps(cfg.Kind),
		host:   host,
		logger: lg,
		tracer: cfg.Tracer,
	}, nil
}

func buildDockerCaps(kind dockerDriverKind) Caps {
	caps := Caps{}
	if kind == dockerKindPodman {
		caps.RootlessCapable = true
	}

	return caps
}

// Kind returns the backend kind.
func (e *dockerEngine) Kind() EngineKind {
	return e.kind
}

// Capabilities returns the non-method-shaped traits of this driver.
func (e *dockerEngine) Capabilities() Caps {
	return e.caps
}

// Ping verifies the daemon is reachable and negotiates the API version.
func (e *dockerEngine) Ping(ctx context.Context) error {
	_, err := e.cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
	if err != nil {
		return fmt.Errorf("docker: ping: %w", err)
	}

	return nil
}

// Close releases resources held by the HTTP client.
func (e *dockerEngine) Close() error {
	return e.cli.Close()
}

// PullImage pulls an image from a registry and waits for completion.
func (e *dockerEngine) PullImage(ctx context.Context, ref string, o PullImageOpts) error {
	e.logger.DebugContext(ctx, "pull image", "ref", ref)
	opts := client.ImagePullOptions{}
	if o.Platform != "" {
		p, err := platforms.Parse(o.Platform)
		if err != nil {
			return fmt.Errorf("%w: platform %q: %w", ErrInvalidSpec, o.Platform, err)
		}
		opts.Platforms = []ocispec.Platform{p}
	}
	resp, err := e.cli.ImagePull(ctx, ref, opts)
	if err != nil {
		return mapDockerErr(fmt.Errorf("docker: pull %s: %w", ref, err))
	}
	if err = resp.Wait(ctx); err != nil {
		return mapDockerErr(fmt.Errorf("docker: pull %s: %w", ref, err))
	}

	return nil
}

// CreateContainer creates a container from spec and returns its ID.
func (e *dockerEngine) CreateContainer(ctx context.Context, spec ContainerSpec) (ContainerID, error) {
	e.logger.DebugContext(ctx, "create container", "image", spec.Image, "name", spec.Name)

	if err := spec.Validate(); err != nil {
		return "", err
	}

	cfg := &container.Config{
		Image:      spec.Image,
		Env:        spec.Env,
		WorkingDir: spec.WorkingDir,
		Labels:     spec.Labels,
	}
	if len(spec.Command) > 0 {
		cfg.Entrypoint = spec.Command
	}
	if len(spec.Args) > 0 {
		cfg.Cmd = spec.Args
	}

	portBindings, err := dockerConvertPorts(spec.Ports)
	if err != nil {
		return "", err
	}

	hc := &container.HostConfig{
		Mounts:        dockerConvertMounts(spec.Mounts),
		PortBindings:  portBindings,
		RestartPolicy: dockerConvertRestartPolicy(spec.Restart),
		Resources: container.Resources{
			NanoCPUs: spec.Resources.NanoCPUs,
			Memory:   spec.Resources.MemoryBytes,
		},
	}

	// Attach the first network at create time so the container is on it
	// before it starts. Additional networks are connected after create.
	var netCfg *network.NetworkingConfig
	if len(spec.Networks) > 0 {
		first := spec.Networks[0]
		netCfg = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				first.Name: {Aliases: first.Aliases},
			},
		}
	}

	result, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           cfg,
		HostConfig:       hc,
		NetworkingConfig: netCfg,
		Name:             spec.Name,
	})
	if err != nil {
		return "", mapDockerErr(fmt.Errorf("docker: create container: %w", err))
	}

	for i := 1; i < len(spec.Networks); i++ {
		n := spec.Networks[i]
		if _, nerr := e.cli.NetworkConnect(ctx, n.Name, client.NetworkConnectOptions{
			Container:      result.ID,
			EndpointConfig: &network.EndpointSettings{Aliases: n.Aliases},
		}); nerr != nil {
			return "", mapDockerErr(fmt.Errorf("docker: connect container %s to network %s: %w", result.ID, n.Name, nerr))
		}
	}

	e.logger.DebugContext(ctx, "container created", "id", result.ID)

	return ContainerID(result.ID), nil
}

// StartContainer starts a previously created container.
func (e *dockerEngine) StartContainer(ctx context.Context, id ContainerID) error {
	e.logger.DebugContext(ctx, "start container", "id", id)
	_, err := e.cli.ContainerStart(ctx, string(id), client.ContainerStartOptions{})
	if err != nil {
		return mapDockerErr(fmt.Errorf("docker: start %s: %w", id, err))
	}

	return nil
}

// StopContainer sends a stop signal to a running container.
func (e *dockerEngine) StopContainer(ctx context.Context, id ContainerID, o StopContainerOpts) error {
	e.logger.DebugContext(ctx, "stop container", "id", id)
	opts := client.ContainerStopOptions{}
	if o.Timeout > 0 {
		secs := int(o.Timeout.Seconds())
		opts.Timeout = &secs
	}
	_, err := e.cli.ContainerStop(ctx, string(id), opts)
	if err != nil {
		return mapDockerErr(fmt.Errorf("docker: stop %s: %w", id, err))
	}

	return nil
}

// RemoveContainer removes a container.
func (e *dockerEngine) RemoveContainer(ctx context.Context, id ContainerID, o RemoveContainerOpts) error {
	e.logger.DebugContext(ctx, "remove container", "id", id)
	_, err := e.cli.ContainerRemove(ctx, string(id), client.ContainerRemoveOptions{
		Force:         o.Force,
		RemoveVolumes: o.RemoveVolumes,
	})
	if err != nil {
		return mapDockerErr(fmt.Errorf("docker: remove %s: %w", id, err))
	}

	return nil
}

// ListContainers returns a list of containers matching the given filters.
func (e *dockerEngine) ListContainers(ctx context.Context, o ListContainersOpts) ([]Container, error) {
	result, err := e.cli.ContainerList(ctx, client.ContainerListOptions{
		All: o.All,
	})
	if err != nil {
		return nil, mapDockerErr(fmt.Errorf("docker: list containers: %w", err))
	}

	out := make([]Container, 0, len(result.Items))
	for _, c := range result.Items {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, Container{
			ID:     ContainerID(c.ID),
			Name:   name,
			Image:  c.Image,
			State:  string(c.State),
			Labels: c.Labels,
		})
	}

	return out, nil
}

// ContainerLogs implements the Logger capability interface.
//
// The returned reader always produces clean, demultiplexed output. For
// containers without a TTY the Docker daemon wraps stdout and stderr in
// 8-byte frame headers; this method transparently strips those headers so
// callers can treat the stream as plain text.
func (e *dockerEngine) ContainerLogs(ctx context.Context, id ContainerID, o ContainerLogsOpts) (io.ReadCloser, error) {
	e.logger.DebugContext(ctx, "container logs", "id", id, "follow", o.Follow)
	tail := "all"
	if o.Tail > 0 {
		tail = strconv.Itoa(o.Tail)
	}
	logsResult, err := e.cli.ContainerLogs(ctx, string(id), client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     o.Follow,
		Tail:       tail,
	})
	if err != nil {
		return nil, mapDockerErr(fmt.Errorf("docker: logs %s: %w", id, err))
	}

	// When the container has a TTY the daemon streams raw bytes (no headers).
	// When it does not, stdout and stderr are framed with 8-byte headers that
	// must be stripped with stdcopy.StdCopy. We inspect once to detect the
	// TTY flag; on error we fall back to assuming no TTY (safer default).
	hasTTY := false
	if insp, ierr := e.cli.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{}); ierr == nil {
		if insp.Container.Config != nil {
			hasTTY = insp.Container.Config.Tty
		}
	}
	if hasTTY {
		return logsResult, nil
	}

	// Demultiplex stdout+stderr into a single reader via an io.Pipe.
	pr, pw := io.Pipe()
	go func() {
		_, copyErr := stdcopy.StdCopy(pw, pw, logsResult)
		_ = logsResult.Close() //nolint:errcheck // best-effort: source already drained
		_ = pw.CloseWithError(copyErr)
	}()

	return &demuxCloser{r: pr, src: logsResult}, nil
}

// demuxCloser wraps an [io.Pipe] reader so that Close also stops the
// background demux goroutine by closing the source stream.
type demuxCloser struct {
	r   *io.PipeReader
	src io.Closer
}

func (d *demuxCloser) Read(p []byte) (int, error) { return d.r.Read(p) }
func (d *demuxCloser) Close() error {
	err := d.src.Close()
	_ = d.r.Close() //nolint:errcheck // pipe reader close is always nil

	return err
}

// Exec implements the Execer capability interface.
func (e *dockerEngine) Exec(ctx context.Context, id ContainerID, o ExecOpts) (ExecResult, error) {
	e.logger.DebugContext(ctx, "exec", "id", id, "cmd", o.Cmd)

	createResult, err := e.cli.ExecCreate(ctx, string(id), client.ExecCreateOptions{
		Cmd:          o.Cmd,
		Env:          o.Env,
		WorkingDir:   o.WorkingDir,
		AttachStdout: o.AttachStdout,
		AttachStderr: o.AttachStderr,
	})
	if err != nil {
		return ExecResult{}, mapDockerErr(fmt.Errorf("docker: exec create %s: %w", id, err))
	}

	attachResult, err := e.cli.ExecAttach(ctx, createResult.ID, client.ExecAttachOptions{})
	if err != nil {
		return ExecResult{}, mapDockerErr(fmt.Errorf("docker: exec attach %s: %w", id, err))
	}
	defer attachResult.Close()

	var stdout, stderr bytes.Buffer
	if _, err = stdcopy.StdCopy(&stdout, &stderr, attachResult.Reader); err != nil {
		return ExecResult{}, fmt.Errorf("docker: exec copy %s: %w", id, err)
	}

	inspect, err := e.cli.ExecInspect(ctx, createResult.ID, client.ExecInspectOptions{})
	if err != nil {
		return ExecResult{}, mapDockerErr(fmt.Errorf("docker: exec inspect %s: %w", id, err))
	}

	result := ExecResult{ExitCode: inspect.ExitCode}
	if o.AttachStdout {
		result.Stdout = &stdout
	}
	if o.AttachStderr {
		result.Stderr = &stderr
	}

	return result, nil
}

// Inspect implements the Inspector capability.
func (e *dockerEngine) Inspect(ctx context.Context, id ContainerID) (ContainerInfo, error) {
	e.logger.DebugContext(ctx, "inspect container", "id", id)
	result, err := e.cli.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if err != nil {
		return ContainerInfo{}, mapDockerErr(fmt.Errorf("docker: inspect %s: %w", id, err))
	}

	c := result.Container
	info := ContainerInfo{
		ID:      ContainerID(c.ID),
		Name:    strings.TrimPrefix(c.Name, "/"),
		ImageID: c.Image,
	}
	if c.Config != nil {
		info.Image = c.Config.Image
		info.Command = append([]string(nil), c.Config.Entrypoint...)
		info.Command = append(info.Command, c.Config.Cmd...)
		info.Env = c.Config.Env
		info.WorkingDir = c.Config.WorkingDir
		info.Labels = c.Config.Labels
	}
	if c.State != nil {
		started, _ := time.Parse(time.RFC3339Nano, c.State.StartedAt)   //nolint:errcheck
		finished, _ := time.Parse(time.RFC3339Nano, c.State.FinishedAt) //nolint:errcheck
		info.State = ContainerState{
			Running:    c.State.Running,
			Paused:     c.State.Paused,
			Restarting: c.State.Restarting,
			ExitCode:   c.State.ExitCode,
			Error:      c.State.Error,
			StartedAt:  started,
			FinishedAt: finished,
		}
	}
	for _, m := range c.Mounts {
		info.Mounts = append(info.Mounts, Mount{
			Type:     MountType(m.Type),
			Source:   m.Source,
			Target:   m.Destination,
			ReadOnly: !m.RW,
		})
	}

	return info, nil
}

// Stats implements the Stater capability.
// It returns a one-shot snapshot of the container's resource usage.
func (e *dockerEngine) Stats(ctx context.Context, id ContainerID, _ StatsOpts) (ContainerStats, error) {
	e.logger.DebugContext(ctx, "container stats", "id", id)
	result, err := e.cli.ContainerStats(ctx, string(id), client.ContainerStatsOptions{Stream: false})
	if err != nil {
		return ContainerStats{}, mapDockerErr(fmt.Errorf("docker: stats %s: %w", id, err))
	}
	defer result.Body.Close() //nolint:errcheck // response body close error ignored

	var raw container.StatsResponse
	if err = json.NewDecoder(result.Body).Decode(&raw); err != nil {
		return ContainerStats{}, fmt.Errorf("docker: decode stats %s: %w", id, err)
	}

	return ContainerStats{
		CPUPercent:    dockerCPUPercent(raw),
		MemoryUsage:   raw.MemoryStats.Usage,
		MemoryLimit:   raw.MemoryStats.Limit,
		NetworkInput:  dockerNetInput(raw),
		NetworkOutput: dockerNetOutput(raw),
	}, nil
}

// dockerCPUPercent calculates the CPU usage percentage from a StatsResponse.
// Returns 0 when delta information is unavailable (e.g. first one-shot sample).
func dockerCPUPercent(s container.StatsResponse) float64 {
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	numCPUs := float64(s.CPUStats.OnlineCPUs)
	if numCPUs == 0 {
		numCPUs = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	if systemDelta <= 0 || cpuDelta < 0 {
		return 0
	}

	return (cpuDelta / systemDelta) * numCPUs * 100.0
}

func dockerNetInput(s container.StatsResponse) uint64 {
	var total uint64
	for _, n := range s.Networks {
		total += n.RxBytes
	}

	return total
}

func dockerNetOutput(s container.StatsResponse) uint64 {
	var total uint64
	for _, n := range s.Networks {
		total += n.TxBytes
	}

	return total
}

// WaitContainer implements the Waiter capability.
func (e *dockerEngine) WaitContainer(ctx context.Context, id ContainerID, o WaitContainerOpts) (<-chan WaitResult, error) {
	cond := container.WaitConditionNotRunning
	switch o.Condition {
	case WaitConditionNextExit:
		cond = container.WaitConditionNextExit
	case WaitConditionRemoved:
		cond = container.WaitConditionRemoved
	case WaitConditionNotRunning, "":
		// default: WaitConditionNotRunning is already set above
	}

	raw := e.cli.ContainerWait(ctx, string(id), client.ContainerWaitOptions{Condition: cond})

	out := make(chan WaitResult, 1)
	go func() {
		defer close(out)
		select {
		case res := <-raw.Result:
			errMsg := ""
			if res.Error != nil {
				errMsg = res.Error.Message
			}
			out <- WaitResult{StatusCode: int(res.StatusCode), Error: errMsg}
		case err := <-raw.Error:
			if err != nil {
				out <- WaitResult{Error: err.Error()}
			}
		case <-ctx.Done():
			out <- WaitResult{Error: ctx.Err().Error()}
		}
	}()

	return out, nil
}

// Events implements the Eventer capability.
func (e *dockerEngine) Events(ctx context.Context) (<-chan Event, error) {
	raw := e.cli.Events(ctx, client.EventsListOptions{})

	out := make(chan Event, 64)
	go func() {
		defer close(out)
		for {
			select {
			case msg, ok := <-raw.Messages:
				if !ok {
					return
				}
				out <- Event{
					Type:   string(msg.Type),
					Action: string(msg.Action),
					Actor:  dockerActorString(msg.Actor),
				}
			case err := <-raw.Err:
				if err != nil {
					e.logger.DebugContext(ctx, "events stream closed", "err", err)
				}

				return
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, nil
}

func dockerActorString(a events.Actor) string {
	if name, ok := a.Attributes["name"]; ok {
		return name
	}

	return a.ID
}

// ListImages implements the Imager capability.
func (e *dockerEngine) ListImages(ctx context.Context, o ListImagesOpts) ([]Image, error) {
	result, err := e.cli.ImageList(ctx, client.ImageListOptions{All: o.All})
	if err != nil {
		return nil, mapDockerErr(fmt.Errorf("docker: list images: %w", err))
	}
	out := make([]Image, 0, len(result.Items))
	for _, img := range result.Items {
		out = append(out, Image{
			ID:        img.ID,
			Tags:      img.RepoTags,
			SizeBytes: img.Size,
		})
	}

	return out, nil
}

// RemoveImage implements the Imager capability.
func (e *dockerEngine) RemoveImage(ctx context.Context, ref string, o RemoveImageOpts) error {
	_, err := e.cli.ImageRemove(ctx, ref, client.ImageRemoveOptions{Force: o.Force})
	if err != nil {
		return mapDockerErr(fmt.Errorf("docker: remove image %s: %w", ref, err))
	}

	return nil
}

// TagImage implements the Imager capability.
func (e *dockerEngine) TagImage(ctx context.Context, src, dst string) error {
	_, err := e.cli.ImageTag(ctx, client.ImageTagOptions{Source: src, Target: dst})
	if err != nil {
		return mapDockerErr(fmt.Errorf("docker: tag image %s -> %s: %w", src, dst, err))
	}

	return nil
}

// CreateNetwork implements the Networker capability.
func (e *dockerEngine) CreateNetwork(ctx context.Context, name string, o CreateNetworkOpts) (NetworkID, error) {
	result, err := e.cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{Driver: o.Driver})
	if err != nil {
		return "", mapDockerErr(fmt.Errorf("docker: create network %s: %w", name, err))
	}

	return NetworkID(result.ID), nil
}

// ListNetworks implements the Networker capability.
func (e *dockerEngine) ListNetworks(ctx context.Context, _ ListNetworksOpts) ([]Network, error) {
	result, err := e.cli.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, mapDockerErr(fmt.Errorf("docker: list networks: %w", err))
	}
	out := make([]Network, 0, len(result.Items))
	for _, n := range result.Items {
		out = append(out, Network{
			ID:     NetworkID(n.ID),
			Name:   n.Name,
			Driver: n.Driver,
		})
	}

	return out, nil
}

// RemoveNetwork implements the Networker capability.
func (e *dockerEngine) RemoveNetwork(ctx context.Context, id NetworkID, _ RemoveNetworkOpts) error {
	_, err := e.cli.NetworkRemove(ctx, string(id), client.NetworkRemoveOptions{})
	if err != nil {
		return mapDockerErr(fmt.Errorf("docker: remove network %s: %w", id, err))
	}

	return nil
}

// ConnectContainer implements the Networker capability.
func (e *dockerEngine) ConnectContainer(ctx context.Context, net NetworkID, id ContainerID, o ConnectOpts) error {
	e.logger.DebugContext(ctx, "connect container to network", "id", id, "network", net)
	_, err := e.cli.NetworkConnect(ctx, string(net), client.NetworkConnectOptions{
		Container:      string(id),
		EndpointConfig: &network.EndpointSettings{Aliases: o.Aliases},
	})
	if err != nil {
		return mapDockerErr(fmt.Errorf("docker: connect container %s to network %s: %w", id, net, err))
	}

	return nil
}

// DisconnectContainer implements the Networker capability.
func (e *dockerEngine) DisconnectContainer(ctx context.Context, net NetworkID, id ContainerID, o DisconnectOpts) error {
	e.logger.DebugContext(ctx, "disconnect container from network", "id", id, "network", net)
	_, err := e.cli.NetworkDisconnect(ctx, string(net), client.NetworkDisconnectOptions{
		Container: string(id),
		Force:     o.Force,
	})
	if err != nil {
		return mapDockerErr(fmt.Errorf("docker: disconnect container %s from network %s: %w", id, net, err))
	}

	return nil
}

// Endpoint implements the EndpointReporter capability.
func (e *dockerEngine) Endpoint() Endpoint {
	return Endpoint{Host: e.host}
}

// CreateVolume implements the Volumer capability.
func (e *dockerEngine) CreateVolume(ctx context.Context, name string, o CreateVolumeOpts) (VolumeID, error) {
	result, err := e.cli.VolumeCreate(ctx, client.VolumeCreateOptions{
		Name:   name,
		Driver: o.Driver,
	})
	if err != nil {
		return "", mapDockerErr(fmt.Errorf("docker: create volume %s: %w", name, err))
	}

	return VolumeID(result.Volume.Name), nil
}

// ListVolumes implements the Volumer capability.
func (e *dockerEngine) ListVolumes(ctx context.Context, _ ListVolumesOpts) ([]Volume, error) {
	result, err := e.cli.VolumeList(ctx, client.VolumeListOptions{})
	if err != nil {
		return nil, mapDockerErr(fmt.Errorf("docker: list volumes: %w", err))
	}
	out := make([]Volume, 0, len(result.Items))
	for _, v := range result.Items {
		out = append(out, Volume{
			ID:         VolumeID(v.Name),
			Driver:     v.Driver,
			Mountpoint: v.Mountpoint,
		})
	}

	return out, nil
}

// RemoveVolume implements the Volumer capability.
func (e *dockerEngine) RemoveVolume(ctx context.Context, id VolumeID, o RemoveVolumeOpts) error {
	_, err := e.cli.VolumeRemove(ctx, string(id), client.VolumeRemoveOptions{Force: o.Force})
	if err != nil {
		return mapDockerErr(fmt.Errorf("docker: remove volume %s: %w", id, err))
	}

	return nil
}

// CopyToContainer implements the Copier capability.
// Content must be a TAR archive reader.
func (e *dockerEngine) CopyToContainer(ctx context.Context, id ContainerID, o CopyToContainerOpts) error {
	_, err := e.cli.CopyToContainer(ctx, string(id), client.CopyToContainerOptions{
		DestinationPath: o.DestPath,
		Content:         o.Content,
	})
	if err != nil {
		return mapDockerErr(fmt.Errorf("docker: copy to container %s: %w", id, err))
	}

	return nil
}

// CopyFromContainer implements the Copier capability.
// Returns a TAR-archived stream.
func (e *dockerEngine) CopyFromContainer(ctx context.Context, id ContainerID, o CopyFromContainerOpts) (io.ReadCloser, error) {
	result, err := e.cli.CopyFromContainer(ctx, string(id), client.CopyFromContainerOptions{
		SourcePath: o.SrcPath,
	})
	if err != nil {
		return nil, mapDockerErr(fmt.Errorf("docker: copy from container %s: %w", id, err))
	}

	return result.Content, nil
}

// mapDockerErr translates moby/containerd error types into the sentinel taxonomy.
func mapDockerErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case cerrdefs.IsNotFound(err):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case cerrdefs.IsAlreadyExists(err):
		return fmt.Errorf("%w: %w", ErrAlreadyExists, err)
	case cerrdefs.IsConflict(err):
		return fmt.Errorf("%w: %w", ErrConflict, err)
	default:
		return err
	}
}

// dockerConvertMounts converts Mount values into moby mount.Mount values.
func dockerConvertMounts(mounts []Mount) []mount.Mount {
	if len(mounts) == 0 {
		return nil
	}
	out := make([]mount.Mount, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, mount.Mount{
			Type:     mount.Type(m.Type),
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}

	return out
}

// dockerConvertPorts converts Port values into a network.PortMap.
// Returns ErrInvalidSpec if any port spec cannot be parsed.
func dockerConvertPorts(ports []Port) (network.PortMap, error) {
	if len(ports) == 0 {
		return nil, nil //nolint:nilnil // intentional: nil map means "no port bindings"
	}
	pm := make(network.PortMap)
	for _, p := range ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		containerPort, err := network.ParsePort(fmt.Sprintf("%d/%s", p.Container, proto))
		if err != nil {
			return nil, fmt.Errorf("%w: port %d/%s: %w", ErrInvalidSpec, p.Container, proto, err)
		}
		binding := network.PortBinding{}
		if p.Host != 0 {
			binding.HostPort = strconv.FormatUint(uint64(p.Host), 10)
		}
		pm[containerPort] = append(pm[containerPort], binding)
	}

	return pm, nil
}

// dockerConvertRestartPolicy converts a RestartPolicy to the Docker type.
func dockerConvertRestartPolicy(rp RestartPolicy) container.RestartPolicy {
	if rp.Mode == "" {
		return container.RestartPolicy{Name: container.RestartPolicyDisabled}
	}

	return container.RestartPolicy{
		Name:              container.RestartPolicyMode(rp.Mode),
		MaximumRetryCount: rp.MaxRetries,
	}
}
