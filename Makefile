.PHONY: build test run clean

build:
	go build -o bin/remind ./cmd/remind

test:
	go test ./...

run:
	go run ./cmd/remind -config ./config.yaml

clean:
	rm -rf bin
