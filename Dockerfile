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
# Stage 2: Runtime.
#
# Layer order is intentional — least-frequently-changed layers first so that
# rebuilding aide (which changes on every code push) does not invalidate the
# claude install layer or any system-package layers.
#
#   node:lts-bookworm-slim   ~90 MB   Node.js pre-baked; no curl/setup dance
#   + ca-certificates        ~2 MB    stable; almost never invalidated
#   + claude install         ~200 MB  invalidated only on claude version bump
#   + user/dirs              <1 MB    stable
#   + aide binary            ~15 MB   invalidated on every code change
#
# Net result: a typical aide rebuild only re-pushes/pulls the last layer.

FROM node:lts-bookworm-slim AS runtime

# System packages — own layer, changes at most when Debian packages update.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# Claude Code — own layer so a code-only aide rebuild does not re-pull claude.
RUN npm install -g @anthropic-ai/claude-code

# Non-root user. UID/GID default to 1000; match the host user so mounted
# volume files have the correct ownership.
ARG UID=1000
ARG GID=1000
RUN groupadd -g ${GID} aide \
 && useradd -u ${UID} -g aide -m -d /home/aide -s /bin/sh aide

# Pre-create mount-point directories with correct ownership.
RUN mkdir -p \
      /home/aide/.aide \
      /home/aide/.aide/sessions \
      /home/aide/.claude \
      /home/aide/.local/bin \
      /home/aide/.local/share/claude/versions \
 && chown -R aide:aide /home/aide

# aide binary — this layer changes on every code push.
COPY --from=builder --chown=root:root /src/aide /usr/local/bin/aide
RUN chmod 755 /usr/local/bin/aide

COPY --chown=root:root docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 755 /usr/local/bin/docker-entrypoint.sh

USER aide
ENV HOME=/home/aide
ENV PATH=/home/aide/.local/bin:/usr/local/bin:/usr/bin:/bin

WORKDIR /home/aide

EXPOSE 8080

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["aide", \
     "--config", "/home/aide/.aide/config.yaml", \
     "--db",     "/home/aide/.aide/aide.db"]
