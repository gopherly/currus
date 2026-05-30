# basic example

Demonstrates the core container lifecycle using auto-detection:

1. Detect the installed container engine (Docker or Podman).
2. Pull `docker.io/library/busybox:latest`.
3. Create and start a container that runs `echo hello from currus`.
4. Read the container logs (if the engine supports it).
5. Remove the container.

## Run

```bash
go run ./examples/basic/...
```

A live Docker or Podman daemon must be reachable on its default socket.
