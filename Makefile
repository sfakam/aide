VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"
BINARY  := aide

.PHONY: build install service service-macos clean test

build:
	go build $(LDFLAGS) -o $(BINARY) .
	@echo ""
	@echo "  Built ./$(BINARY) — run 'make install' to update /usr/local/bin/$(BINARY)"
	@echo ""

install: build
	sudo install -m 755 $(BINARY) /usr/local/bin/$(BINARY)
	@echo "Installed /usr/local/bin/$(BINARY)"

# Install the systemd user service and create ~/.aide with a starter config.
# Idempotent: safe to re-run; existing config.yaml is never overwritten.
service: install
	@mkdir -p ~/.aide ~/.config/systemd/user
	@if [ ! -f ~/.aide/config.yaml ]; then \
		cp config.example.yaml ~/.aide/config.yaml; \
		echo "Created ~/.aide/config.yaml — fill in your credentials"; \
	else \
		echo "~/.aide/config.yaml already exists — not overwritten"; \
	fi
	@if [ ! -f ~/.aide/tasks.yaml ]; then \
		cp tasks.example.yaml ~/.aide/tasks.yaml; \
		echo "Created ~/.aide/tasks.yaml — edit to enable/configure scheduled tasks"; \
	else \
		echo "~/.aide/tasks.yaml already exists — not overwritten"; \
	fi
	install -m 644 aide.service ~/.config/systemd/user/aide.service
	systemctl --user daemon-reload
	systemctl --user enable aide
	@echo ""
	@echo "  Done. Edit ~/.aide/config.yaml, then run:"
	@echo "    systemctl --user start aide"
	@echo "    journalctl --user -fu aide"
	@echo ""

# Install as a launchd user agent on macOS.
# Idempotent: safe to re-run; existing config/tasks files are never overwritten.
service-macos: install
	@mkdir -p ~/.aide ~/Library/LaunchAgents
	@if [ ! -f ~/.aide/config.yaml ]; then \
		cp config.example.yaml ~/.aide/config.yaml; \
		echo "Created ~/.aide/config.yaml — fill in your credentials"; \
	else \
		echo "~/.aide/config.yaml already exists — not overwritten"; \
	fi
	@if [ ! -f ~/.aide/tasks.yaml ]; then \
		cp tasks.example.yaml ~/.aide/tasks.yaml; \
		echo "Created ~/.aide/tasks.yaml — edit to enable/configure scheduled tasks"; \
	else \
		echo "~/.aide/tasks.yaml already exists — not overwritten"; \
	fi
	sed "s|__HOME__|$$HOME|g" aide.plist > ~/Library/LaunchAgents/local.aide.plist
	launchctl unload ~/Library/LaunchAgents/local.aide.plist 2>/dev/null || true
	launchctl load -w ~/Library/LaunchAgents/local.aide.plist
	@echo ""
	@echo "  Done. Edit ~/.aide/config.yaml, then run:"
	@echo "    launchctl start local.aide"
	@echo "    tail -f /tmp/aide.err"
	@echo ""

test:
	go test -v -race ./...

clean:
	rm -f $(BINARY)
