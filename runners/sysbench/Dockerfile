FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod ./
COPY main.go credentials.go metrics.go ./

RUN CGO_ENABLED=0 GOOS=linux go build \
	-trimpath \
	-ldflags="-s -w" \
	-o /out/sysbench-runner \
	.

FROM debian:bookworm-slim

# Configuration encoding and summary parsing target sysbench 1.0.20. Its Debian
# package includes the Lua workloads and pulls in the MySQL client library.
RUN set -eu; \
	apt-get update; \
	apt-get install -y --no-install-recommends sysbench=1.0.20+ds-5; \
	rm -rf /var/lib/apt/lists/*; \
	sysbench --version; \
	sysbench --version | grep -Eq '^sysbench 1\.0\.20$'; \
	sysbench --help | grep -Eq '^[[:space:]]*mysql - MySQL driver$'; \
	sysbench oltp_read_write help | grep -F 'oltp_read_write options:'; \
	dpkg-query -W -f='${Package}=${Version}\n' sysbench libmariadb3; \
	groupadd --gid 10001 runner; \
	useradd --uid 10001 --gid runner --no-create-home \
		--home-dir /tmp --shell /usr/sbin/nologin runner

COPY --from=builder /out/sysbench-runner /usr/local/bin/sysbench-runner

USER runner:runner
WORKDIR /tmp

# Credential files are created privately inside the writable temporary directory.
RUN test "$(id -u)" -ne 0 && test -w /tmp

ENTRYPOINT ["/usr/local/bin/sysbench-runner"]
