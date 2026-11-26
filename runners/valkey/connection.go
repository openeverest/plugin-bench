package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	connectTimeout = 10 * time.Second
	keyBatchSize   = 256
)

type valkeyEndpoint struct {
	address   string
	tlsConfig *tls.Config
	user      string
	password  string
}

func newValkeyEndpoint(c config, caPath string) (valkeyEndpoint, error) {
	endpoint := valkeyEndpoint{
		address: net.JoinHostPort(c.host, strconv.Itoa(c.port)),
		user:    c.username, password: c.password,
	}
	if c.tls {
		pem, err := os.ReadFile(caPath)
		roots := x509.NewCertPool()
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			return valkeyEndpoint{}, errors.New("could not load the Valkey CA")
		}
		endpoint.tlsConfig = &tls.Config{
			RootCAs: roots, ServerName: strings.TrimSuffix(c.host, "."), MinVersion: tls.VersionTLS12,
		}
	}
	return endpoint, nil
}

func (e valkeyEndpoint) connect(ctx context.Context) (net.Conn, *bufio.Reader, error) {
	dialer := net.Dialer{Timeout: connectTimeout}
	var conn net.Conn
	var err error
	if e.tlsConfig != nil {
		dialer := tls.Dialer{NetDialer: &dialer, Config: e.tlsConfig}
		conn, err = dialer.DialContext(ctx, "tcp", e.address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", e.address)
	}
	if err != nil {
		return nil, nil, errors.New("could not connect to Valkey or verify its TLS certificate")
	}
	deadline := time.Now().Add(connectTimeout)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return nil, nil, errors.New("could not set the Valkey connection deadline")
	}
	reader := bufio.NewReader(conn)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	reply, err := valkeyCommand(conn, reader, "AUTH", e.user, e.password)
	stop()
	if ctx.Err() != nil {
		conn.Close()
		return nil, nil, ctx.Err()
	}
	if err != nil || reply != "+OK" {
		conn.Close()
		return nil, nil, errors.New("Valkey authentication failed")
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, nil, errors.New("could not clear the Valkey connection deadline")
	}
	return conn, reader, nil
}

// Read only bounded, single-line RESP replies. Server responses may contain
// credentials, so callers report errors without including the response.
func valkeyCommand(conn net.Conn, reader *bufio.Reader, args ...string) (string, error) {
	var request strings.Builder
	fmt.Fprintf(&request, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(&request, "$%d\r\n%s\r\n", len(arg), arg)
	}
	if _, err := io.WriteString(conn, request.String()); err != nil {
		return "", err
	}
	reply, err := reader.ReadSlice('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(reply), "\r\n"), nil
}

func verifySeed(ctx context.Context, endpoint valkeyEndpoint, prefix string, keyCount int) error {
	conn, reader, err := endpoint.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	for first := 1; first <= keyCount; first += keyBatchSize {
		last := min(first+keyBatchSize-1, keyCount)
		args := make([]string, 1, last-first+2)
		args[0] = "EXISTS"
		for key := first; key <= last; key++ {
			args = append(args, prefix+strconv.Itoa(key))
		}
		reply, err := valkeyCommand(conn, reader, args...)
		if err != nil || reply != ":"+strconv.Itoa(last-first+1) {
			return errors.New("Valkey did not confirm that all benchmark keys were seeded")
		}
	}
	return nil
}

func removeBenchmarkKeys(ctx context.Context, endpoint valkeyEndpoint, prefix string, keyCount int) error {
	conn, reader, err := endpoint.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	args := make([]string, 1, keyBatchSize+1)
	args[0] = "UNLINK"
	for first := 1; first <= keyCount; first += keyBatchSize {
		last := min(first+keyBatchSize-1, keyCount)
		args = args[:1]
		for key := first; key <= last; key++ {
			args = append(args, prefix+strconv.Itoa(key))
		}
		reply, err := valkeyCommand(conn, reader, args...)
		if err != nil {
			return errors.New("could not complete Valkey UNLINK")
		}
		count, err := strconv.Atoi(strings.TrimPrefix(reply, ":"))
		// Some keys may never have been seeded, or may already have expired.
		if !strings.HasPrefix(reply, ":") || err != nil || count < 0 || count > len(args)-1 {
			return errors.New("Valkey did not confirm UNLINK; check that the benchmark user can run it")
		}
	}
	return nil
}

// Memtier connects through a private Unix socket. Each forwarded connection
// authenticates independently, with hostname and CA verification when TLS is enabled.
func serveValkey(ctx context.Context, listener net.Listener, endpoint valkeyEndpoint, fail context.CancelCauseFunc) {
	var clients sync.WaitGroup
	defer clients.Wait()
	for {
		local, err := listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				fail(errors.New("could not accept a Memtier connection"))
			}
			return
		}
		clients.Go(func() {
			defer local.Close()
			upstream, reader, err := endpoint.connect(ctx)
			if err != nil {
				fail(err)
				return
			}
			defer upstream.Close()
			stop := context.AfterFunc(ctx, func() { upstream.Close(); local.Close() })
			defer stop()
			done := make(chan struct{})
			go func() {
				defer close(done)
				io.Copy(upstream, local)
				upstream.Close()
			}()
			io.Copy(local, reader)
			local.Close()
			<-done
		})
	}
}
