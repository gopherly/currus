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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContainerSpecValidate verifies that Validate rejects specs with missing
// required fields and accepts well-formed ones.
func TestContainerSpecValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    ContainerSpec
		wantErr error
	}{
		{
			name: "valid minimal spec",
			spec: ContainerSpec{Image: "alpine:latest"},
		},
		{
			name: "valid spec with all fields",
			spec: ContainerSpec{
				Image:      "nginx:1.25",
				Name:       "web",
				Command:    []string{"/bin/sh"},
				Args:       []string{"-c", "echo hello"},
				Env:        []string{"FOO=bar"},
				WorkingDir: "/app",
				Labels:     map[string]string{"env": "test"},
			},
		},
		{
			name: "valid spec with security and DNS fields",
			spec: ContainerSpec{
				Image:      "alpine:latest",
				Hostname:   "app-host",
				ExtraHosts: []string{"db:10.0.0.1"},
				Init:       true,
				Security: Security{
					User:             "1000:1000",
					Groups:           []string{"docker"},
					Privileged:       false,
					AddCapabilities:  []Capability{CapNetBindService},
					DropCapabilities: []Capability{CapAll},
					SecurityOpts:     []string{"no-new-privileges"},
				},
				DNS: DNS{
					Servers: []string{"8.8.8.8"},
					Search:  []string{"example.com"},
					Options: []string{"ndots:5"},
				},
			},
		},
		{
			name: "privileged spec is valid",
			spec: ContainerSpec{
				Image:    "alpine:latest",
				Security: Security{Privileged: true},
			},
		},
		{
			name:    "empty image returns ErrInvalidSpec",
			spec:    ContainerSpec{},
			wantErr: ErrInvalidSpec,
		},
		{
			name:    "name-only with empty image returns ErrInvalidSpec",
			spec:    ContainerSpec{Name: "my-container"},
			wantErr: ErrInvalidSpec,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.spec.Validate()
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
