.PHONY: build test vet fmt run-sim run-bridge clean

build:
	go build -o bin/bridge ./cmd/bridge
	go build -o bin/simulator ./cmd/simulator

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

run-sim:
	go run ./cmd/simulator

run-bridge:
	go run ./cmd/bridge

clean:
	rm -rf bin
