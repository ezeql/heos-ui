.PHONY: run dev fmt lint test race cover vuln tidy

# The app: the web UI. The speaker is discovered when none is cached.
run:
	go run ./cmd/ui

# The app with hot reload, rebuilding on every .go/.html change. Note that
# ../goheos lives outside this repo, so edits there still need a restart.
dev:
	@command -v air >/dev/null || { \
		echo "air is not installed: go install github.com/air-verse/air@latest"; \
		exit 1; \
	}
	air

fmt:
	golangci-lint fmt

lint:
	golangci-lint run

test:
	go test ./...

race:
	go test -race -count=1 ./...

cover:
	go test -race -covermode=atomic -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

tidy:
	go mod tidy
	git diff --exit-code go.mod
