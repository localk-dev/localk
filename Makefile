.PHONY: build test test-update lint clean run-example install

GO ?= go
BIN := bin/localk

build:
	$(GO) build -trimpath -ldflags "-s -w" -o $(BIN) ./cmd/localk

test:
	$(GO) test -race -cover ./...

# Regenerate golden files after an intentional change to the converter.
test-update:
	$(GO) test ./internal/convert -update

lint:
	$(GO) vet ./...
	$(GO) fmt ./...

clean:
	rm -rf bin dist docker-compose.yml .env

# Run the binary against the bundled example and print the result.
run-example: build
	./$(BIN) generate ./examples/simple/k8s/ -o /tmp/localk-example.yml -env-out /tmp/localk-example.env
	@echo
	@echo "--- compose ---"
	@cat /tmp/localk-example.yml
	@echo "--- env ---"
	@cat /tmp/localk-example.env

install:
	$(GO) install ./cmd/localk
