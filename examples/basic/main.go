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

// Command basic demonstrates the core container lifecycle: auto-detect the
// engine, pull an image, create and start a container, read its logs, then
// clean up.
//
// Usage:
//
//	go run ./examples/basic/...
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"

	"gopherly.dev/currus"
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Zero-config: detect whatever container engine is installed.
	// MustNew panics when no engine is reachable. For production code
	// that should handle missing engines gracefully, use currus.New instead.
	eng := currus.MustNew(ctx, currus.WithLogger(logger))
	defer func() {
		if err := eng.Close(); err != nil {
			logger.Error("close engine", "err", err)
		}
	}()

	logger.Info("engine detected", "kind", eng.Engine())

	const image = "docker.io/library/busybox:latest"

	logger.Info("pulling image", "ref", image)
	if err := eng.PullImage(ctx, image, currus.PullImageOpts{}); err != nil {
		logger.Error("pull failed", "err", err)
		return err
	}

	logger.Info("creating container")
	id, err := eng.CreateContainer(ctx, currus.ContainerSpec{
		Image:   image,
		Name:    "currus-basic-example",
		Command: []string{"echo"},
		Args:    []string{"hello from currus"},
	})
	if err != nil {
		logger.Error("create container failed", "err", err)
		return err
	}
	defer func() {
		logger.Info("removing container", "id", id)
		if rmErr := eng.RemoveContainer(ctx, id, currus.RemoveContainerOpts{Force: true}); rmErr != nil {
			if !errors.Is(rmErr, currus.ErrNotFound) {
				logger.Error("remove container failed", "err", rmErr)
			}
		}
	}()

	logger.Info("starting container", "id", id)
	if err = eng.StartContainer(ctx, id); err != nil {
		logger.Error("start container failed", "err", err)
		return err
	}

	// Read logs if the engine supports the Logger capability.
	if lg, ok := eng.(currus.Logger); ok {
		printContainerLogs(ctx, logger, lg, id)
	} else {
		logger.Info("engine does not support container logs")
	}

	logger.Info("done")
	return nil
}

// printContainerLogs streams a container's logs to stdout.
func printContainerLogs(ctx context.Context, logger *slog.Logger, lg currus.Logger, id currus.ContainerID) {
	logger.InfoContext(ctx, "reading logs", "id", id)
	rc, err := lg.ContainerLogs(ctx, id, currus.ContainerLogsOpts{Follow: false, Tail: 20})
	if err != nil {
		logger.WarnContext(ctx, "logs not available", "err", err)
		return
	}
	defer rc.Close() //nolint:errcheck // example cleanup
	if _, err = io.Copy(os.Stdout, rc); err != nil {
		logger.WarnContext(ctx, "copy logs", "err", err)
	}
}
