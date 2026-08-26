VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"
BINARY  := aide

.PHONY: build install clean test

build:
	go build $(LDFLAGS) -o $(BINARY) .
	@echo ""
	@echo "  Built ./$(BINARY) — run 'make install' to update /usr/local/bin/$(BINARY)"
	@echo ""

install: build
	sudo install -m 755 $(BINARY) /usr/local/bin/$(BINARY)
	@echo "Installed /usr/local/bin/$(BINARY)"

test:
	go test -v -race ./...

clean:
	rm -f $(BINARY)
