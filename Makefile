.PHONY: test fmt migrate run

test:
	go test -race -count=1 ./...
fmt:
	gofmt -w cmd internal
migrate:
	sh scripts/migrate.sh
run:
	go run ./cmd/server
