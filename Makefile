.PHONY: build test integration benchmark benchmark-integration lint generate check-generate snapshot

build:
	CGO_ENABLED=0 go build -trimpath -o relaycat ./cmd/relaycat

test:
	go test -race -shuffle=on ./...

integration:
	go test -race -count=1 -tags=integration ./...

benchmark:
	go test -run '^$$' -bench . -benchmem -benchtime=1s -count=5 ./internal/accesscode ./internal/tunnelcrypto

benchmark-integration:
	go test -run '^$$' -tags=integration -bench EndToEnd -benchmem -benchtime=1s -count=5 ./internal/endpoint

lint:
	buf lint
	go vet ./...
	golangci-lint run

generate:
	buf generate

check-generate: generate
	git diff --exit-code -- gen

snapshot:
	goreleaser release --snapshot --clean
