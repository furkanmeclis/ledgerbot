# syntax=docker/dockerfile:1.7
# Pinned to go.mod's version: a host with an older cached 1.26 image would
# otherwise fail with "go.mod requires go >= …".
FROM golang:1.26.10-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ledgerbot ./cmd/ledgerbot

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/ledgerbot /app/ledgerbot
COPY seed /app/seed
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app/ledgerbot"]
CMD ["all"]
