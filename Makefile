BINARY := livecode
VERSION ?= 0.1.0
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test lint clean release
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -rf $(BINARY) dist

release:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/livecode-linux-amd64 .
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/livecode-linux-arm64 .
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/livecode-darwin-amd64 .
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/livecode-darwin-arm64 .
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/livecode-windows-amd64.exe .
