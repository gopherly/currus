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

package conformance

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsRegistryRateLimit verifies the rate-limit heuristic string matching.
func TestIsRegistryRateLimit(t *testing.T) {
	t.Parallel()

	var (
		errTooManyRequests = errors.New("toomanyrequests")                       //nolint:err113
		errRateLimitLower  = errors.New("rate limit exceeded")                   //nolint:err113
		errPullRateLimit   = errors.New("You have reached your pull rate limit") //nolint:err113
		errCaseInsensitive = errors.New("TooManyRequests from registry")         //nolint:err113
		errUnrelated       = errors.New("connection refused")                    //nolint:err113
		errEmpty           = errors.New("")                                      //nolint:err113
	)

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"toomanyrequests", errTooManyRequests, true},
		{"rate limit lower", errRateLimitLower, true},
		{"pull rate limit", errPullRateLimit, true},
		{"case-insensitive", errCaseInsensitive, true},
		{"unrelated error", errUnrelated, false},
		{"empty message", errEmpty, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, isRegistryRateLimit(tc.err))
		})
	}
}
