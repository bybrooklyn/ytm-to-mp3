set shell := ["bash", "-cu"]

version := `git describe --tags --always --dirty 2>/dev/null || echo dev`

default:
    @just --list

# build ./mfy
build:
    go build -ldflags "-s -w -X main.version={{version}}" -o mfy ./cmd/mfy

# build ./mfy-desktop, the native desktop window (Fyne: needs a C compiler and OpenGL/X11 headers on Linux)
build-desktop:
    CGO_ENABLED=1 go build -ldflags "-s -w" -o mfy-desktop ./cmd/mfy-desktop

# cross-compile mfy.exe for Windows (amd64)
build-win:
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-s -w -X main.version={{version}}" -o mfy.exe ./cmd/mfy

# install to $GOBIN / ~/go/bin (make sure it's on PATH), then use `mfy <url>`
install:
    go install -ldflags "-s -w -X main.version={{version}}" ./cmd/mfy
    @echo "installed: $(go env GOBIN | grep . || echo $(go env GOPATH)/bin)/mfy"

# install to ~/.local/bin
install-local: build
    mkdir -p "${MFY_INSTALL_DIR:-$HOME/.local/bin}"
    install -m755 mfy "${MFY_INSTALL_DIR:-$HOME/.local/bin}/mfy"

# download URLs/exports: just get <url>... (extra args go to mfy)
get *args:
    go run ./cmd/mfy {{args}}

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
    rm -rf mfy mfy.exe mfy-desktop dist

