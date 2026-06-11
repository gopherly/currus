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
	"io"
	"os"
	"strings"
	"testing"

	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/dind"

	"gopherly.dev/currus"
	"gopherly.dev/currus/conformance"
)

// TestConformanceDinD runs the conformance suite against an isolated Docker
// daemon inside a Docker-in-Docker container. Skipped in -short mode or when
// CURRUS_TEST_ENGINE is not "docker".
func TestConformanceDinD(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping DinD test in short mode")
	}

	if engine := os.Getenv("CURRUS_TEST_ENGINE"); engine != "" && engine != "docker" {
		t.Skipf("skipping DinD test: CURRUS_TEST_ENGINE=%s (only runs for docker)", engine)
	}

	ctx := context.Background()

	dindC, err := dind.Run(ctx, "docker:27-dind")
	if err != nil {
		t.Skipf("cannot start DinD container (Docker unavailable?): %v", err)
	}
	t.Cleanup(func() {
		if termErr := testcontainers.TerminateContainer(dindC); termErr != nil {
			t.Logf("terminate DinD container: %v", termErr)
		}
	})

	rawHost, err := dindC.Host(ctx)
	if err != nil {
		t.Fatalf("get DinD host: %v", err)
	}
	dockerHost := strings.Replace(rawHost, "http://", "tcp://", 1)

	seedImageIntoDinD(t, ctx, dockerHost)

	conformance.Run(t, func(t *testing.T) currus.Engine {
		t.Helper()

		eng, engErr := currus.New(ctx,
			currus.WithEngine(currus.Docker),
			currus.WithEndpoint(currus.Endpoint{Host: dockerHost}),
		)
		if engErr != nil {
			t.Fatalf("create engine against DinD: %v", engErr)
		}
		t.Cleanup(func() {
			if closeErr := eng.Close(); closeErr != nil {
				t.Logf("close engine: %v", closeErr)
			}
		})

		return eng
	}, conformance.RunOpts{SkipPortMapping: true})
}

// seedImageIntoDinD copies TestImage from the host daemon into the DinD daemon
// via save/load, avoiding registry pulls (and rate limits) inside DinD.
func seedImageIntoDinD(t *testing.T, ctx context.Context, dockerHost string) {
	t.Helper()

	hostCli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("create host docker client: %v", err)
	}
	defer func() {
		if closeErr := hostCli.Close(); closeErr != nil {
			t.Logf("close host docker client: %v", closeErr)
		}
	}()

	if _, inspErr := hostCli.ImageInspect(ctx, conformance.TestImage); inspErr != nil {
		pullResp, pullErr := hostCli.ImagePull(ctx, conformance.TestImage, client.ImagePullOptions{})
		if pullErr != nil {
			t.Fatalf("pull %s on host: %v", conformance.TestImage, pullErr)
		}
		if waitErr := pullResp.Wait(ctx); waitErr != nil {
			t.Fatalf("wait for pull of %s: %v", conformance.TestImage, waitErr)
		}
		if err := pullResp.Close(); err != nil {
			t.Logf("close pull response: %v", err)
		}
	}

	saveRC, err := hostCli.ImageSave(ctx, []string{conformance.TestImage})
	if err != nil {
		t.Fatalf("save %s from host: %v", conformance.TestImage, err)
	}
	defer saveRC.Close()

	dindCli, err := client.NewClientWithOpts(
		client.WithHost(dockerHost),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		t.Fatalf("create dind docker client: %v", err)
	}
	defer func() {
		if closeErr := dindCli.Close(); closeErr != nil {
			t.Logf("close dind docker client: %v", closeErr)
		}
	}()

	loadRC, err := dindCli.ImageLoad(ctx, saveRC)
	if err != nil {
		t.Fatalf("load %s into dind: %v", conformance.TestImage, err)
	}
	if _, copyErr := io.Copy(io.Discard, loadRC); copyErr != nil {
		t.Logf("drain load response: %v", copyErr)
	}
	if err := loadRC.Close(); err != nil {
		t.Logf("close load response: %v", err)
	}
}
