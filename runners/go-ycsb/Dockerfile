FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS upstream-builder

RUN apk add --no-cache ca-certificates
WORKDIR /src

# Build the pinned v1.0.3 source for the selected target architecture.
RUN set -eu; \
	wget -O /tmp/go-ycsb.tar.gz \
		https://codeload.github.com/pingcap/go-ycsb/tar.gz/f030f9942393a8febbf4365c2d582711723159f5; \
	echo '51703e27d7934931eeb7b79312b517f7ab697182211b549428e8e23c97a80300  /tmp/go-ycsb.tar.gz' | sha256sum -c -; \
	tar -xzf /tmp/go-ycsb.tar.gz --strip-components=1 -C /src; \
	rm /tmp/go-ycsb.tar.gz

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build \
	-mod=readonly -trimpath -ldflags="-s -w" \
	-o /out/go-ycsb ./cmd/go-ycsb

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS wrapper-builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go credentials.go output.go metrics.go ./

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build \
	-mod=readonly -trimpath -ldflags="-s -w" \
	-o /out/go-ycsb-runner .

FROM debian:bookworm-slim

RUN set -eu; \
	apt-get update; \
	apt-get install -y --no-install-recommends ca-certificates; \
	rm -rf /var/lib/apt/lists/*; \
	groupadd --gid 10001 runner; \
	useradd --uid 10001 --gid runner --no-create-home \
		--home-dir /tmp --shell /usr/sbin/nologin runner

COPY --from=upstream-builder /out/go-ycsb /usr/local/bin/go-ycsb
COPY --from=upstream-builder /src/LICENSE /src/YCSB-LICENSE /usr/share/licenses/go-ycsb/
COPY --from=wrapper-builder /out/go-ycsb-runner /usr/local/bin/go-ycsb-runner
COPY workloads/workloada /etc/go-ycsb/workloads/workloada

USER runner:runner
WORKDIR /tmp

# Private credential files need writable temporary storage. Check the packaged
# tool and workload while running as the same user used for benchmarks.
RUN test "$(id -u)" -eq 10001 \
	&& test -w /tmp \
	&& test -x /usr/local/bin/go-ycsb-runner \
	&& test -s /etc/go-ycsb/workloads/workloada \
	&& go-ycsb run --help > /dev/null

ENTRYPOINT ["/usr/local/bin/go-ycsb-runner"]
