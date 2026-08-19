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

# Mount a volume here to keep loot.db across restarts.
VOLUME ["/data"]
ENV LOOT_DATA_DIR=/data \
    LOOT_LISTEN=:8080

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/loot"]
CMD ["serve"]
