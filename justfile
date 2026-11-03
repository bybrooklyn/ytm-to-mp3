set shell := ["bash", "-cu"]

version := `git describe --tags --always --dirty 2>/dev/null || echo dev`

default:
    @just --list

# build ./ytm
build:
    go build -ldflags "-s -w -X main.version={{version}}" -o ytm ./cmd/ytm

# install to $GOBIN / ~/go/bin (make sure it's on PATH), then use `ytm <url>`
install:
    go install -ldflags "-s -w -X main.version={{version}}" ./cmd/ytm
    @echo "installed: $(go env GOBIN | grep . || echo $(go env GOPATH)/bin)/ytm"

# install to ~/.local/bin
install-local: build
    install -Dm755 ytm "${YTM_INSTALL_DIR:-$HOME/.local/bin}/ytm"

# download URLs/exports: just get <url>... (extra args go to ytm)
get *args:
    go run ./cmd/ytm {{args}}

test:
    go test ./...

check: fmt-check vet test

vet:
    go vet ./...

fmt:
    gofmt -w .

fmt-check:
    @test -z "$(gofmt -l .)" || { gofmt -l .; exit 1; }

# local multi-platform snapshot build
snapshot:
    goreleaser release --snapshot --clean

clean:
    rm -rf ytm ytm.exe dist

# bring every saved VPN up and show exit IPs
vpn-test:
    go run ./cmd/ytm vpn test
