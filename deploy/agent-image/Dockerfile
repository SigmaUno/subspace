FROM golang:1.27-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/subspace-join ./cmd/subspace-join && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/subspace-hook ./cmd/subspace-hook && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/subspace-egress ./cmd/subspace-egress

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS egress
COPY --from=build /out/subspace-egress /usr/local/bin/subspace-egress
USER 65532:65532
EXPOSE 3128
ENTRYPOINT ["/usr/local/bin/subspace-egress"]

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS agent
RUN apk add --no-cache ca-certificates git openssh-client && \
    addgroup -S -g 65532 agent && \
    adduser -S -D -u 65532 -G agent -h /work agent && \
    mkdir -p /work/repo /run/secrets && \
    chown -R 65532:65532 /work
COPY --from=build /out/subspace-join /out/subspace-hook /usr/local/bin/
COPY --chmod=755 deploy/agent-image/entrypoint.sh /usr/local/bin/agent-entrypoint
ENV HOME=/work SUBSPACE_TOKEN_FILE=/run/secrets/subspace_token
USER 65532:65532
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/agent-entrypoint"]
