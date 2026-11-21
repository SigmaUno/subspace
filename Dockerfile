FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS backup
RUN apk add --no-cache sqlite
COPY --chmod=755 deploy/backup.sh /usr/local/bin/subspace-backup
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/subspace-backup"]

FROM golang:1.27-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/subspace ./cmd/subspace && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/subspacectl ./cmd/subspacectl

WORKDIR /probe
COPY <<'EOF' main.go
package main

import (
	"net/http"
	"os"
	"time"
)

func main() {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(os.Args[1])
	if err != nil || resp.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}
EOF
RUN CGO_ENABLED=0 GO111MODULE=off go build -trimpath -ldflags="-s -w" -o /out/probe main.go

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS server
COPY --from=build /out/subspace /usr/local/bin/subspace
COPY --from=build /out/subspacectl /usr/local/bin/subspacectl
COPY --from=build /out/probe /usr/local/bin/probe
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/subspace"]
