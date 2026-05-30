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

package currus_test

import (
	"testing"

	"gopherly.dev/currus"
	"gopherly.dev/currus/currustest"
)

// BenchmarkCreateRemoveContainer measures the per-operation overhead of the
// fake engine's container lifecycle path. This exercises Currus's own code
// (argument conversion, mutex contention) without any daemon I/O, giving a
// stable baseline for comparing driver overhead in integration runs.
func BenchmarkCreateRemoveContainer(b *testing.B) {
	eng := currustest.New()
	ctx := b.Context()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id, err := eng.CreateContainer(ctx, currus.ContainerSpec{
			Image: "busybox:latest",
			Name:  "",
		})
		if err != nil {
			b.Fatal(err)
		}
		if err = eng.RemoveContainer(ctx, id, currus.RemoveContainerOpts{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkListContainers measures the cost of listing containers as the
// store grows. The benchmark pre-populates N/2 containers so the list call
// has something non-trivial to traverse.
func BenchmarkListContainers(b *testing.B) {
	ctx := b.Context()
	eng := currustest.New()

	// Pre-populate 100 containers.
	const preload = 100
	for range preload {
		if _, err := eng.CreateContainer(ctx, currus.ContainerSpec{Image: "busybox:latest"}); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := eng.ListContainers(ctx, currus.ListContainersOpts{All: true}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPullImage measures PullImage on the in-memory fake (pure map write).
func BenchmarkPullImage(b *testing.B) {
	eng := currustest.New()
	ctx := b.Context()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := eng.PullImage(ctx, "busybox:latest", currus.PullImageOpts{}); err != nil {
			b.Fatal(err)
		}
	}
}
