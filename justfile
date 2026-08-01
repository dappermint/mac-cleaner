# list available recipes
default:
    @just --list

# run the tui against your own home directory
run:
    go run .

# run the tui with root inventory (System Data, macOS, other users)
run-root:
    sudo go run . --root

# print the storage surface without opening the tui
surface depth="3":
    go run . surface --depth {{ depth }}

# walk the whole surface as root and check the filesystem
verify:
    sudo go run . surface --root --verify

# run the test suite
test:
    go test ./...

# format, vet and test
check: fmt
    go vet ./...
    go test ./...

# format go and nix sources
fmt:
    gofmt -w .
    nixfmt flake.nix package.nix

# lint go sources
lint:
    golangci-lint run

# build the binary into ./result via nix
nix-build:
    nix build --print-out-paths

# run the nix-built binary, e.g. just nix-run surface --depth 4
nix-run *args:
    nix run . -- {{ args }}

# build with nix, then run the result as root for the full inventory
nix-run-root *args: nix-build
    sudo ./result/bin/mac-cleaner --root {{ args }}

# check that every flake output evaluates and builds
nix-check:
    nix flake check

# update flake.lock inputs
nix-update:
    nix flake update

# install the tool into the user's nix profile
install:
    nix profile install "git+file://$(pwd)"

# remove build artefacts
clean:
    rm -rf result
    go clean -cache -testcache
