.PHONY: build test fmt lint dist install clean demo

# Default version comes from release-please manifest so fork builds stay aligned
# with upstream release cadence after merges.
MANIFEST_VERSION := $(shell sed -n 's/.*": "\([^"]*\)".*/\1/p' .release-please-manifest.json 2>/dev/null)
VERSION ?= $(or $(MANIFEST_VERSION),dev)
ifeq ($(VERSION),dev)
LDFLAGS := -X main.version=dev
else
LDFLAGS := -X main.version=v$(VERSION)
endif

build:
	go build -ldflags "$(LDFLAGS)" -o treehouse .

test:
	go test ./...

fmt:
	gofmt -w .

lint:
	gofmt -l .
	go vet ./...

dist:
	@mkdir -p dist
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/treehouse-darwin-arm64 .
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/treehouse-darwin-amd64 .
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/treehouse-linux-arm64 .
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/treehouse-linux-amd64 .
	GOOS=windows GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/treehouse-windows-arm64.exe .
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/treehouse-windows-amd64.exe .

install: build
	cp treehouse $(GOPATH)/bin/ 2>/dev/null || cp treehouse /usr/local/bin/

demo: build
	vhs demo.tape

clean:
	rm -rf treehouse dist/ coverage.out
