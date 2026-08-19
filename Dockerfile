# syntax=docker/dockerfile:1

# ---------------------------------------------------------------- web build
FROM node:22-alpine AS web

WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci || npm install

COPY web/ ./
RUN npm run build

# ---------------------------------------------------------------- go build
FROM golang:1.26-alpine AS go

WORKDIR /src

# Module cache layer: only re-downloads when go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# The compiled SPA replaces the .gitkeep placeholder before the embed runs.
COPY --from=web /src/web/dist ./web/dist

# An empty, correctly owned data directory, so the image can write its database
# even when nobody mounts a volume — which is exactly how demo mode is run.
RUN mkdir -p /data

ARG VERSION=docker
# modernc.org/sqlite is pure Go, so a static binary needs no cgo toolchain.
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/loot ./cmd/loot

# ---------------------------------------------------------------- runtime
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /
COPY --from=go /out/loot /loot
COPY --from=go /src/configs/loot.example.yaml /etc/loot/loot.example.yaml

COPY --from=go --chown=nonroot:nonroot /data /data

# Mount a volume here to keep loot.db across restarts. Demo mode needs no
# volume at all: docker run -p 8080:8080 -e LOOT_DEMO=1 ghcr.io/nickhirras/loot
VOLUME ["/data"]
ENV LOOT_DATA_DIR=/data \
    LOOT_LISTEN=:8080

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/loot"]
CMD ["serve"]
