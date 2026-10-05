VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)
DIST    ?= dist

.PHONY: exe test lint resources run-dev clean

# The whole product: one self-installing Windows program.
exe:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS) -H=windowsgui' -o $(DIST)/Windstream.exe ./cmd/windstream

test:
	go test -race ./...

lint:
	go vet ./...
	GOOS=windows go vet ./...
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed on:"; gofmt -l .; exit 1)

# Regenerate the exe icon/manifest resources and the tray icon.
resources:
	cd cmd/windstream && go run ../../tools/genres

# The full app (dashboard on http://127.0.0.1:47333) with a test pattern;
# changes nothing on the system. Works on any OS with ffmpeg installed.
run-dev:
	go run ./cmd/windstream app -dev -data dev-data

clean:
	rm -rf $(DIST) dev-data
