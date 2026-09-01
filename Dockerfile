# Stage 1: Build aide for the target platform.
# --platform=$BUILDPLATFORM keeps the compiler on the native build arch;
# GOOS/GOARCH cross-compile the output for the container's target arch.
FROM --platform=$BUILDPLATFORM golang:1.22-bookworm AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "-X main.version=${VERSION}" -o aide .

# ─────────────────────────────────────────────────────────────────────────────
# Stage 2: Minimal runtime image.
# debian:bookworm-slim provides glibc (required by the claude ELF binary
# mounted from the host) without adding unnecessary packages.
FROM debian:bookworm-slim AS runtime

# ca-certificates is needed for HTTPS calls aide or claude may make.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# Non-root user. UID/GID default to 1000 but should match the host user
# so that mounted volume files have the right ownership.
ARG UID=1000
ARG GID=1000
RUN groupadd -g ${GID} aide \
 && useradd -u ${UID} -g aide -m -d /home/aide -s /bin/sh aide

# Pre-create every directory that will be used as a mount point.
# Ownership is set here; bind mounts will overlay these at runtime.
RUN mkdir -p \
      /home/aide/.aide \
      /home/aide/.aide/sessions \
      /home/aide/.claude \
      /home/aide/.local/bin \
      /home/aide/.local/share/claude/versions \
 && chown -R aide:aide /home/aide

COPY --from=builder --chown=root:root /src/aide /usr/local/bin/aide
RUN chmod 755 /usr/local/bin/aide

COPY --chown=root:root docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 755 /usr/local/bin/docker-entrypoint.sh

USER aide
ENV HOME=/home/aide
ENV PATH=/home/aide/.local/bin:/usr/local/bin:/usr/bin:/bin

WORKDIR /home/aide

# aide API (optional — disabled by passing --api "" in CMD)
EXPOSE 8080

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["aide", \
     "--config", "/home/aide/.aide/config.yaml", \
     "--db",     "/home/aide/.aide/aide.db"]
