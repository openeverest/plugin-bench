FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/valkey-runner \
    .

# Keep Memtier 2.6.1 and its runtime libraries together for both architectures.
FROM redislabs/memtier_benchmark@sha256:41849040f6ea0b335c31ae532b1488ce47ea6e8c48a086d661f3d6953d3b599d

COPY --from=builder /out/valkey-runner /usr/local/bin/valkey-runner

ENV TMPDIR=/tmp
USER 10001:10001
WORKDIR /tmp

RUN set -eu; \
    test -w /tmp; \
    version="$(memtier_benchmark --version 2>&1)"; \
    printf '%s\n' "$version" | grep -E '(^|[[:space:]])(v=)?2\.6\.1([[:space:]]|$)'

ENTRYPOINT ["/usr/local/bin/valkey-runner"]
