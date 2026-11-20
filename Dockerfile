FROM alpine:3.24 AS backup
RUN apk add --no-cache sqlite
COPY --chmod=755 deploy/backup.sh /usr/local/bin/subspace-backup
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/subspace-backup"]

FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/subspace ./cmd/subspace && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/subspacectl ./cmd/subspacectl

FROM gcr.io/distroless/static-debian12:nonroot AS server
COPY --from=build /out/subspace /usr/local/bin/subspace
COPY --from=build /out/subspacectl /usr/local/bin/subspacectl
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/subspace"]
