# list available recipes
default:
    @just --list

# everything a new contributor needs before touching code
setup:
    @command -v nix >/dev/null || { echo "install nix first: https://nixos.org/download"; exit 1; }
    @command -v direnv >/dev/null && direnv allow || echo "no direnv, use 'nix develop' instead"
    @echo "ready. 'just' lists recipes, 'just verify' is what ci runs"

# what ci runs, run this before opening a pull request
verify: fmt
    go vet ./...
    GOOS=darwin GOARCH=amd64 go vet ./...
    golangci-lint run
    go test -race -count=1 ./...
    nix flake check

# run the tui against your own home directory
run:
    go run ./cmd/ratatouille

# run the tui with root inventory (System Data, macOS, other users)
run-root:
    sudo go run ./cmd/ratatouille --root

# print the storage surface without opening the tui
surface depth="3":
    go run ./cmd/ratatouille surface --depth {{ depth }}

# walk the whole surface as root and check the filesystem
health:
    sudo go run ./cmd/ratatouille surface --root --verify

# cross-compile both darwin arches and fuse them into a universal binary
build-universal:
    GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o dist/rat-arm64 ./cmd/ratatouille
    GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -o dist/rat-amd64 ./cmd/ratatouille
    lipo -create -output dist/ratatouille dist/rat-arm64 dist/rat-amd64
    lipo -info dist/ratatouille

# vet and build for intel as well, which ci does not otherwise cover locally
check-amd64:
    GOOS=darwin GOARCH=amd64 go vet ./...
    GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null ./cmd/ratatouille

# run the test suite
test:
    go test ./...

# run one test by name, e.g. just test-one TestSurfaceWalkAccountsEveryByte
test-one name:
    go test -run {{ name }} -v ./...

# test with the race detector and a coverage profile
test-cover:
    go test -race -covermode=atomic -coverprofile=coverage.out ./...
    go tool cover -func=coverage.out | tail -1

# open the coverage profile in a browser
cover-html: test-cover
    go tool cover -html=coverage.out

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
    sudo ./result/bin/ratatouille --root {{ args }}

# check that every flake output evaluates and builds
nix-check:
    nix flake check

# update flake.lock inputs
nix-update:
    nix flake update

# install the tool into the user's nix profile
install:
    nix profile install "git+file://$(pwd)"

# dry-run the release pipeline without publishing anything
release-check:
    goreleaser release --snapshot --clean --skip=publish

# remove build artefacts
clean:
    rm -rf result dist coverage.out
    go clean -cache -testcache
