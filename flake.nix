{
  description = "Currus — unified Go interface for every container engine (dev shell)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    git-hooks = {
      url = "github:cachix/git-hooks.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
      git-hooks,
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs { inherit system; };

        devTools = with pkgs; [
          go
          gopls
          gotools
          golangci-lint
          markdownlint-cli
          delve
          git
        ];

        mkApp =
          {
            name,
            description,
            script,
          }:
          {
            type = "app";
            program = toString (pkgs.writeShellScript name script);
            meta = {
              mainProgram = name;
              inherit description;
            };
          };

        mkTaggedRaceTest =
          {
            name,
            description,
            tags,
            coverProfile,
            integrationTestsAtModuleRoot ? false,
          }:
          let
            goListCmd =
              if integrationTestsAtModuleRoot then
                ''"$go" list -tags=${tags} .''
              else
                ''"$go" list -tags=${tags} ./...'';
          in
          mkApp {
            inherit name description;
            script = ''
              export GOTOOLCHAIN=local
              go="${pkgs.go}/bin/go"
              mapfile -t testpkgs < <(${goListCmd} | grep -vE '/examples(/|$)' || true)
              if [ ''${#testpkgs[@]} -eq 0 ]; then
                echo "go list: no test packages after filters (tags=${tags})" >&2
                exit 1
              fi
              exec "$go" test -tags=${tags} -race -shuffle=on -covermode=atomic \
                -coverpkg=./... -coverprofile=${coverProfile} -timeout 10m "''${testpkgs[@]}"
            '';
          };

        pre-commit-check = git-hooks.lib.${system}.run {
          src = ./.;
          hooks = {
            golangci-lint = {
              enable = true;
              extraPackages = [ pkgs.go ];
            };
            markdownlint = {
              enable = true;
              excludes = [ "node_modules" ];
              settings.configuration = builtins.fromJSON (builtins.readFile ./.markdownlint.json);
            };
            go-mod-tidy = {
              enable = true;
              name = "go-mod-tidy";
              entry = "${pkgs.go}/bin/go mod tidy";
              files = "(\\.go|go\\.mod|go\\.sum)$";
              pass_filenames = false;
            };
            nixfmt.enable = true;
          };
        };
      in
      {
        formatter = pkgs.nixfmt-tree;

        checks = {
          pre-commit = pre-commit-check;
        };

        devShells.default = pkgs.mkShell {
          name = "currus";
          packages = devTools ++ pre-commit-check.enabledPackages;
          env = {
            GO111MODULE = "on";
            CGO_ENABLED = "1";
          };
          shellHook = ''
            ${pre-commit-check.shellHook}
            export GOPATH="''${GOPATH:-$HOME/go}"
            export PATH="$GOPATH/bin:$PATH"
            echo "Currus dev shell — $(go version)"
          '';
        };

        apps = {
          fmt = mkApp {
            name = "fmt";
            description = "Format Go files (gofumpt + gci via golangci-lint)";
            script = ''
              exec ${pkgs.golangci-lint}/bin/golangci-lint fmt ./...
            '';
          };

          tidy = mkApp {
            name = "tidy";
            description = "Run go mod tidy for the module";
            script = ''
              exec ${pkgs.go}/bin/go mod tidy
            '';
          };

          lint = mkApp {
            name = "lint";
            description = "Run golangci-lint";
            script = ''
              exec ${pkgs.golangci-lint}/bin/golangci-lint run ./...
            '';
          };

          lint-md = mkApp {
            name = "lint-md";
            description = "Lint Markdown files with markdownlint";
            script = ''
              exec ${pkgs.markdownlint-cli}/bin/markdownlint '**/*.md'
            '';
          };

          test-unit = mkTaggedRaceTest {
            name = "test-unit";
            description = "Run unit tests with race detector; write coverage-unit.out (build tag !integration)";
            tags = "!integration";
            coverProfile = "coverage-unit.out";
          };

          test-podman = mkApp {
            name = "test-podman";
            description = "Start an ephemeral rootless Podman socket and run integration tests against it";
            script = ''
              set -euo pipefail
              podman="${pkgs.podman}/bin/podman"
              go="${pkgs.go}/bin/go"

              sock_dir="$(mktemp -d)"
              sock="$sock_dir/podman.sock"
              trap 'kill "$svc_pid" 2>/dev/null; rm -rf "$sock_dir"' EXIT

              "$podman" system service --time=0 "unix://$sock" &
              svc_pid=$!

              # wait for the socket to appear
              for i in $(seq 1 30); do
                [ -S "$sock" ] && break
                sleep 0.2
              done
              if [ ! -S "$sock" ]; then
                echo "podman socket did not appear at $sock" >&2
                exit 1
              fi

              export DOCKER_HOST="unix://$sock"
              export CURRUS_TEST_ENGINE=podman
              export GOTOOLCHAIN=local

              mapfile -t testpkgs < <("$go" list -tags=integration ./... | grep -vE '/examples(/|$)' || true)
              "$go" test -tags=integration -race -shuffle=on -covermode=atomic \
                -coverpkg=./... -coverprofile=coverage-podman.out \
                -timeout 10m "''${testpkgs[@]}"
            '';
          };

          test-docker = mkApp {
            name = "test-docker";
            description = "Start an ephemeral rootless Docker daemon and run integration tests against it";
            script = ''
              set -euo pipefail
              dockerd="${pkgs.docker}/bin/dockerd-rootless"
              go="${pkgs.go}/bin/go"

              run_dir="$(mktemp -d)"
              sock="$run_dir/docker.sock"
              trap 'kill "$svc_pid" 2>/dev/null; wait "$svc_pid" 2>/dev/null; rm -rf "$run_dir" 2>/dev/null || true' EXIT

              export XDG_RUNTIME_DIR="$run_dir"
              "$dockerd" \
                --host "unix://$sock" \
                --data-root "$run_dir/data" \
                2>"$run_dir/dockerd.log" &
              svc_pid=$!

              for i in $(seq 1 60); do
                [ -S "$sock" ] && break
                sleep 0.5
              done
              if [ ! -S "$sock" ]; then
                echo "dockerd-rootless did not start; log:" >&2
                cat "$run_dir/dockerd.log" >&2
                exit 1
              fi

              export DOCKER_HOST="unix://$sock"
              export CURRUS_TEST_ENGINE=docker
              export GOTOOLCHAIN=local

              mapfile -t testpkgs < <("$go" list -tags=integration ./... | grep -vE '/examples(/|$)' || true)
              "$go" test -tags=integration -race -shuffle=on -covermode=atomic \
                -coverpkg=./... -coverprofile=coverage-docker.out \
                -timeout 10m "''${testpkgs[@]}"
            '';
          };

          test-containerd = mkApp {
            name = "test-containerd";
            description = "Start an ephemeral containerd daemon (requires sudo) and run integration tests against it";
            script = ''
              set -euo pipefail
              containerd_bin="${pkgs.containerd}/bin/containerd"
              go="${pkgs.go}/bin/go"
              export PATH="${pkgs.runc}/bin:$PATH"

              state_dir="$(mktemp -d)"
              sock="$state_dir/containerd.sock"
              trap 'sudo kill "$svc_pid" 2>/dev/null; wait "$svc_pid" 2>/dev/null; sudo rm -rf "$state_dir" 2>/dev/null || true' EXIT

              sudo env PATH="$PATH" "$containerd_bin" \
                --address "$sock" \
                --state "$state_dir/state" \
                --root "$state_dir/root" \
                2>"$state_dir/containerd.log" &
              svc_pid=$!

              for i in $(seq 1 60); do
                [ -S "$sock" ] && break
                sleep 0.5
              done
              if [ ! -S "$sock" ]; then
                echo "containerd did not start; log:" >&2
                cat "$state_dir/containerd.log" >&2
                exit 1
              fi

              export CONTAINERD_ADDRESS="$sock"
              export CURRUS_TEST_ENGINE=containerd
              export GOTOOLCHAIN=local

              mapfile -t testpkgs < <("$go" list -tags=integration ./... | grep -vE '/examples(/|$)' || true)
              sudo -E "$go" test -tags=integration -race -shuffle=on -covermode=atomic \
                -coverpkg=./... -coverprofile=coverage-containerd.out \
                -timeout 10m "''${testpkgs[@]}"
            '';
          };

          test-dind = mkApp {
            name = "test-dind";
            description = "Start an ephemeral Podman socket and run TestConformanceDinD against it";
            script = ''
              set -euo pipefail
              podman="${pkgs.podman}/bin/podman"
              go="${pkgs.go}/bin/go"

              sock_dir="$(mktemp -d)"
              sock="$sock_dir/podman.sock"
              trap 'kill "$svc_pid" 2>/dev/null; rm -rf "$sock_dir"' EXIT

              "$podman" system service --time=0 "unix://$sock" &
              svc_pid=$!

              for i in $(seq 1 30); do
                [ -S "$sock" ] && break
                sleep 0.2
              done
              if [ ! -S "$sock" ]; then
                echo "podman socket did not appear at $sock" >&2
                exit 1
              fi

              export DOCKER_HOST="unix://$sock"
              export CURRUS_TEST_ENGINE=docker
              export GOTOOLCHAIN=local

              mapfile -t testpkgs < <("$go" list -tags=integration ./... | grep -vE '/examples(/|$)' || true)
              "$go" test -tags=integration -race -shuffle=on -covermode=atomic \
                -coverpkg=./... -coverprofile=coverage-dind.out \
                -run TestConformanceDinD \
                -timeout 10m "''${testpkgs[@]}"
            '';
          };
        };
      }
    );
}
