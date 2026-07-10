.PHONY: build test install lint

build:
	go build ./...

test:
	go test ./...

lint:
	gofmt -l . && go vet ./...

install:
	go install ./cmd/whittle
