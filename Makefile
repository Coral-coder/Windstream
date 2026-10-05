VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)
DIST    ?= dist

.PHONY: windows test lint run-test clean

# windstream.exe  - console build: CLI commands and interactive `serve`
# windstreamw.exe - GUI-subsystem build: no console window, for autostart
windows:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(DIST)/windstream.exe ./cmd/windstream
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS) -H=windowsgui' -o $(DIST)/windstreamw.exe ./cmd/windstream
	cp deploy/install.ps1 deploy/uninstall.ps1 configs/windstream.example.toml $(DIST)/

test:
	go test -race ./...

lint:
	go vet ./...
	GOOS=windows go vet ./...
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed on:"; gofmt -l .; exit 1)

# Runs the server with the synthetic test source on any OS (needs ffmpeg).
run-test:
	@test -f dev.toml || (echo 'create dev.toml: display.mode = "test", audio.backend = "test", tls.self_signed = true'; exit 1)
	go run ./cmd/windstream serve -config dev.toml

clean:
	rm -rf $(DIST)
