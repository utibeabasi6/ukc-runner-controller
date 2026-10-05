.PHONY: build generate lint test image

build: generate
	go build -o bin/ukc-runner-controller ./cmd/ukc-runner-controller

generate:
	go tool templ generate -include-version=false

lint:
	golangci-lint run ./...

test:
	go test -race ./...

image:
	unikraft build ./image --output $(IMAGE)
