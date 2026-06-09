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
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestResolveDaemonSocket covers every detection branch of resolveDaemonSocket.
func TestResolveDaemonSocket(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot determine home dir: %v", err)
	}

	vmForwardedSock := filepath.Join(home, ".lima", "default", "sock", "docker.sock")
	colimaForwardedSock := filepath.Join(home, ".colima", "default", "docker.sock")

	tests := []struct {
		name         string
		host         string
		override     string
		envVal       string
		wantOnLinux  string
		wantOnDarwin string
	}{
		{
			name:         "native linux standard socket",
			host:         "unix:///var/run/docker.sock",
			wantOnLinux:  "/var/run/docker.sock",
			wantOnDarwin: "/var/run/docker.sock",
		},
		{
			name:         "native linux rootless socket",
			host:         "unix:///run/user/1000/docker.sock",
			wantOnLinux:  "/run/user/1000/docker.sock",
			wantOnDarwin: "/var/run/docker.sock",
		},
		{
			name:         "linux vm-forwarded lima socket under home",
			host:         "unix://" + vmForwardedSock,
			wantOnLinux:  "/var/run/docker.sock",
			wantOnDarwin: "/var/run/docker.sock",
		},
		{
			name:         "linux vm-forwarded colima socket under home",
			host:         "unix://" + colimaForwardedSock,
			wantOnLinux:  "/var/run/docker.sock",
			wantOnDarwin: "/var/run/docker.sock",
		},
		{
			name:         "bare socket path (containerd style)",
			host:         "/run/containerd/containerd.sock",
			wantOnLinux:  "/run/containerd/containerd.sock",
			wantOnDarwin: "/var/run/docker.sock",
		},
		{
			name:         "tcp scheme returns empty",
			host:         "tcp://docker-host:2376",
			wantOnLinux:  "",
			wantOnDarwin: "",
		},
		{
			name:         "ssh scheme returns empty",
			host:         "ssh://user@remote-host",
			wantOnLinux:  "",
			wantOnDarwin: "",
		},
		{
			name:         "npipe scheme returns empty",
			host:         "npipe:////./pipe/docker_engine",
			wantOnLinux:  "",
			wantOnDarwin: "",
		},
		{
			name:         "override takes priority over all heuristics",
			host:         "unix://" + vmForwardedSock,
			override:     "/custom/daemon.sock",
			wantOnLinux:  "/custom/daemon.sock",
			wantOnDarwin: "/custom/daemon.sock",
		},
		{
			name:         "override takes priority on darwin too",
			host:         "unix:///var/run/docker.sock",
			override:     "/custom/daemon.sock",
			wantOnLinux:  "/custom/daemon.sock",
			wantOnDarwin: "/custom/daemon.sock",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			want := tc.wantOnLinux
			if runtime.GOOS == "darwin" {
				want = tc.wantOnDarwin
			}

			got := resolveDaemonSocket(tc.host, tc.override)
			assert.Equal(t, want, got)
		})
	}
}

// TestResolveDaemonSocket_EnvVar verifies that CURRUS_DAEMON_SOCKET takes
// priority over all heuristics. This test cannot run in parallel because it
// modifies the environment.
func TestResolveDaemonSocket_EnvVar(t *testing.T) {
	t.Setenv(daemonSocketEnvVar, "/env/override.sock")

	got := resolveDaemonSocket("unix:///var/run/docker.sock", "")
	assert.Equal(t, "/env/override.sock", got)
}

// TestResolveDaemonSocket_OverrideBeatEnvVar verifies that the programmatic
// override takes priority over the environment variable.
func TestResolveDaemonSocket_OverrideBeatEnvVar(t *testing.T) {
	t.Setenv(daemonSocketEnvVar, "/env/override.sock")

	got := resolveDaemonSocket("unix:///var/run/docker.sock", "/code/override.sock")
	assert.Equal(t, "/code/override.sock", got)
}
