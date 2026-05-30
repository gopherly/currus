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

//go:build integration

package conformance_test

import (
	"context"
	"os"
	"testing"

	"gopherly.dev/currus"
	"gopherly.dev/currus/conformance"
)

// TestConformanceIntegration runs the conformance suite against a real engine
// daemon. The engine is selected by the CURRUS_TEST_ENGINE environment variable
// (docker|podman|containerd). If the daemon is unreachable the test is skipped
// gracefully via t.Skip.
func TestConformanceIntegration(t *testing.T) {
	t.Parallel()

	engineName := os.Getenv("CURRUS_TEST_ENGINE")
	if engineName == "" {
		engineName = "docker"
	}

	conformance.Run(t, func(t *testing.T) currus.Engine {
		t.Helper()
		return newIntegrationEngine(t, currus.EngineKind(engineName))
	})
}

func newIntegrationEngine(t *testing.T, kind currus.EngineKind) currus.Engine {
	t.Helper()
	ctx := context.Background()

	eng, err := currus.New(ctx, currus.WithEngine(kind))
	if err != nil {
		t.Skipf("skipping integration test: cannot create %s engine: %v", kind, err)
	}

	if err := eng.Ping(ctx); err != nil {
		_ = eng.Close()
		t.Skipf("skipping integration test: %s engine not reachable: %v", kind, err)
	}

	t.Cleanup(func() { _ = eng.Close() })
	return eng
}
