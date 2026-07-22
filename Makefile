BIN := bin/db-query
INSTALL_DIR := $(HOME)/.local/bin

.PHONY: build install vet test cover integration integration-up integration-test integration-down clean

build:
	go build -o $(BIN) ./cmd/db-query

# Build, then copy the binary onto PATH at ~/.local/bin (created if absent).
install: build
	mkdir -p $(INSTALL_DIR)
	cp $(BIN) $(INSTALL_DIR)/db-query

vet:
	go vet ./...

test:
	go test ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

integration-up:
	docker compose -f integration/docker-compose.yml up -d --wait

integration-test:
	go test -tags integration -count=1 -timeout 15m -v ./integration/...

integration-down:
	docker compose -f integration/docker-compose.yml down -v

# Full cycle: bring the databases up, run both suites, always tear down.
integration: integration-up
	$(MAKE) integration-test && $(MAKE) integration-down || ($(MAKE) integration-down; exit 1)

clean:
	rm -rf bin coverage.out
