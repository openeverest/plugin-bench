FROM golang:1.22-alpine AS builder

WORKDIR /src

COPY go.mod ./
COPY main.go ./

RUN CGO_ENABLED=0 GOOS=linux go build \
	-trimpath \
	-ldflags="-s -w" \
	-o /out/pgbench-runner \
	.

FROM postgres:16-alpine

# pgbench is provided by PostgreSQL's contrib package rather than the server
# package. Verify it during the image build so the runner cannot start without
# its benchmark executable.
RUN apk add --no-cache postgresql16-contrib \
	&& pgbench --version

COPY --from=builder --chown=postgres:postgres /out/pgbench-runner /usr/local/bin/pgbench-runner

USER postgres

ENTRYPOINT ["/usr/local/bin/pgbench-runner"]
