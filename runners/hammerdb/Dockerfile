FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod ./
COPY *.go phase.tcl ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/hammerdb-runner \
    .

# The upstream image already contains the architecture-specific MySQL library.
# Copy only that library; the digest pins both AMD64 and ARM64 variants.
FROM tpcorg/hammerdb:v6.1.1-mysql@sha256:e9f6f2dae328de97dcd81427be6c711f5b818be8191d9f6efa977a9c04a1abb6 AS mysql-client
ARG TARGETARCH
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) libdir=/usr/lib/x86_64-linux-gnu; machine='Advanced Micro Devices X86-64' ;; \
      arm64) libdir=/usr/lib/aarch64-linux-gnu; machine='AArch64' ;; \
      *) echo "Unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    readelf -h "$libdir/libmysqlclient.so.24" | grep -F 'Machine:' | grep -F "$machine"; \
    readelf -d "$libdir/libmysqlclient.so.24" | grep -E 'SONAME.*\[libmysqlclient\.so\.24\]'; \
    install -D -m 0755 "$libdir/libmysqlclient.so.24" /out/libmysqlclient.so.24

FROM tpcorg/hammerdb:v6.1.1-base@sha256:1bf2882b9462834105bc39cb850327596a19c706863f6e31db9e2ac2119bcda9

# Match upstream's PGDG Noble/libpq 17 dependency. Only client libraries are
# required: HammerDB supplies the Tcl bindings and workload implementations.
RUN set -eu; \
    apt-get update; \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ca-certificates curl gnupg libssl3t64 libzstd1 zlib1g libstdc++6; \
    install -d -m 0755 /etc/apt/keyrings; \
    curl --fail --location --silent --show-error --connect-timeout 15 --max-time 120 \
      https://www.postgresql.org/media/keys/ACCC4CF8.asc -o /tmp/postgresql.asc; \
    fingerprint="$(gpg --batch --with-colons --show-keys /tmp/postgresql.asc | awk -F: '$1 == "fpr" {print $10; exit}')"; \
    test "$fingerprint" = B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8; \
    gpg --batch --dearmor -o /etc/apt/keyrings/postgresql.gpg /tmp/postgresql.asc; \
    rm /tmp/postgresql.asc; \
    echo 'deb [signed-by=/etc/apt/keyrings/postgresql.gpg] https://apt.postgresql.org/pub/repos/apt noble-pgdg main 17' > /etc/apt/sources.list.d/pgdg.list; \
    printf 'Package: libpq5\nPin: version 17.*\nPin-Priority: 1001\n' > /etc/apt/preferences.d/libpq17; \
    apt-get update; \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends 'libpq5=17.*'; \
    apt-get purge -y --auto-remove curl gnupg; \
    rm -rf /var/lib/apt/lists/*; \
    groupadd --gid 10001 runner; \
    useradd --uid 10001 --gid runner --no-create-home \
      --home-dir /tmp --shell /usr/sbin/nologin runner

COPY --from=mysql-client /out/libmysqlclient.so.24 /usr/local/lib/libmysqlclient.so.24
COPY --from=builder /out/hammerdb-runner /usr/local/bin/hammerdb-runner
RUN set -eu; \
    ldconfig; \
    ldconfig -p | grep -F libmysqlclient.so.24; \
    ldconfig -p | grep -F libpq.so.5; \
    ldd /usr/local/lib/libmysqlclient.so.24 > /tmp/mysql-dependencies; \
    ! grep -F 'not found' /tmp/mysql-dependencies; \
    rm /tmp/mysql-dependencies; \
    test -x /home/hammerdb/hammerdbcli

ENV PATH="/home/hammerdb:${PATH}"
WORKDIR /home/hammerdb
USER runner:runner

# runStaged creates private scripts/state under /tmp and removes them on exit.
RUN test "$(id -u)" -ne 0 && test -w /tmp && test -x ./hammerdbcli
ENTRYPOINT ["/usr/local/bin/hammerdb-runner"]
CMD []
