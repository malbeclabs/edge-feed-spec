# syntax=docker/dockerfile:1.6
# Multi-stage build for dz-conformance. The module is self-contained, so the
# build context is this directory (tools/conformance) — see docker-compose.yml.

FROM golang:1.25-alpine AS build
WORKDIR /src

# Cache deps first.
COPY go.mod go.sum ./
RUN go mod download

# Build the binary.
COPY . .
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 go build \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/dz-conformance .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tini
COPY --from=build /out/dz-conformance /usr/local/bin/dz-conformance
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/dz-conformance"]
