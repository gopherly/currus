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
	"runtime"
	"strings"
)

// defaultDaemonSocket is the well-known Docker socket path inside a VM or on
// native Linux.
const defaultDaemonSocket = "/var/run/docker.sock"

// daemonSocketEnvVar is the environment variable that overrides DaemonSocket
// resolution. It takes the highest priority.
const daemonSocketEnvVar = "CURRUS_DAEMON_SOCKET"

// resolveDaemonSocket returns the bind-mountable socket path for the given
// engine host URI. The override parameter comes from [WithDaemonSocket] and
// is applied before any heuristics.
//
// Resolution order:
//  1. override (non-empty) or CURRUS_DAEMON_SOCKET env var
//  2. GOOS == "darwin" → /var/run/docker.sock (Docker cannot run natively on
//     macOS; every daemon runs inside a VM regardless of the forwarding path)
//  3. Socket path under $HOME → /var/run/docker.sock (VM-forwarded socket on
//     Linux: Docker Desktop, Lima, Colima forwarding to the user home dir)
//  4. Otherwise → bare socket path stripped of the "unix://" scheme
//  5. Non-unix scheme (tcp://, ssh://, npipe://) → "" (cannot bind-mount)
func resolveDaemonSocket(host, override string) string {
	// 1. Explicit override wins unconditionally.
	if override != "" {
		return override
	}
	if env := os.Getenv(daemonSocketEnvVar); env != "" {
		return env
	}

	// Extract the raw socket path from a unix:// URI, or accept a bare path.
	var path string
	if p, ok := strings.CutPrefix(host, "unix://"); ok {
		path = p
	} else if strings.HasPrefix(host, "/") {
		path = host
	} else {
		// Non-unix scheme (tcp, ssh, npipe) — bind-mounting is not possible.
		return ""
	}

	// 2. macOS: Docker always lives inside a VM; the daemon socket is always
	//    /var/run/docker.sock from the container's perspective.
	if runtime.GOOS == "darwin" {
		return defaultDaemonSocket
	}

	// 3. Home-dir heuristic: paths under $HOME are VM-forwarded sockets on
	//    Linux (Docker Desktop, Lima, Colima). Native Linux and rootless Docker
	//    use paths outside $HOME (e.g. /var/run/... or /run/user/<uid>/...).
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if strings.HasPrefix(path, home+"/") {
			return defaultDaemonSocket
		}
	}

	// 4. Native socket path — use as-is.
	return path
}
