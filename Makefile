.PHONY: build test install lint

build:
	go build ./...

test:
	go test ./...

lint:
	gofmt -l . && go vet ./...

install:
	go install ./cmd/whittle

hero: ## regenerate the README hero (demo/hero.svg) from real output
	sh demo/make-hero.sh
