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

func newValkeyEndpoint(c config, files privateFiles) (valkeyEndpoint, error) {
	endpoint := valkeyEndpoint{
		address: net.JoinHostPort(c.host, strconv.Itoa(c.port)),
		user:    c.username, password: c.password,
	}
	if c.tls {
		pem, err := os.ReadFile(files.caPath)
		roots := x509.NewCertPool()
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			return valkeyEndpoint{}, errors.New("could not load the Valkey CA")
		}
		endpoint.tlsConfig = &tls.Config{
			RootCAs: roots, ServerName: strings.TrimSuffix(c.host, "."), MinVersion: tls.VersionTLS12,
		}
		if files.certPath != "" {
			certificate, err := tls.LoadX509KeyPair(files.certPath, files.keyPath)
			if err != nil {
				return valkeyEndpoint{}, errors.New("VALKEY_TLS_CERT and VALKEY_TLS_KEY must contain a valid PEM client certificate and matching private key")
			}
			endpoint.tlsConfig.Certificates = []tls.Certificate{certificate}
		}
	}
	return endpoint, nil
}

// Preflight verifies TLS, authenticates, and checks the endpoint topology.
func (e valkeyEndpoint) preflight(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	conn, reader, err := e.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return errors.New("could not set the Valkey preflight deadline")
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	// ROLE leaves part of its response unread, so check cluster mode first.
	if err := checkClusterMode(conn, reader); err != nil {
		return err
	}
	return checkPrimary(conn, reader)
}

func checkClusterMode(conn net.Conn, reader *bufio.Reader) error {
	header, err := valkeyCommand(conn, reader, "INFO", "cluster")
	if strings.HasPrefix(header, "-NOPERM ") {
		return errors.New("Valkey benchmark user must have permission to run INFO cluster")
	}
	length, lengthErr := strconv.Atoi(strings.TrimPrefix(header, "$"))
	// INFO cluster is a short RESP bulk string. Bound the server-supplied length.
	if err != nil || !strings.HasPrefix(header, "$") || lengthErr != nil || length < 1 || length > 4096 {
		return errors.New("could not verify Valkey cluster mode")
	}
	reply := make([]byte, length+2)
	if _, err := io.ReadFull(reader, reply); err != nil || string(reply[length:]) != "\r\n" {
		return errors.New("could not verify Valkey cluster mode")
	}
	for _, line := range strings.Split(string(reply[:length]), "\r\n") {
		switch line {
		case "cluster_enabled:0":
			return nil
		case "cluster_enabled:1":
			return errors.New("Valkey cluster mode is not supported. Use a non-cluster primary endpoint")
		}
	}
	return errors.New("could not verify Valkey cluster mode")
}

func checkPrimary(conn net.Conn, reader *bufio.Reader) error {
	header, err := valkeyCommand(conn, reader, "ROLE")
	if strings.HasPrefix(header, "-NOPERM ") {
		return errors.New("Valkey benchmark user must have permission to run ROLE")
	}
	count, countErr := strconv.Atoi(strings.TrimPrefix(header, "*"))
	if err != nil || !strings.HasPrefix(header, "*") || countErr != nil || count < 1 {
		return errors.New("could not verify Valkey endpoint role")
	}
	// Read the first bulk string only. Preflight closes this connection.
	length, err := readRESPLine(reader)
	if err != nil || (length != "$6" && length != "$5" && length != "$8") {
		return errors.New("could not verify Valkey endpoint role")
	}
	role, err := readRESPLine(reader)
	if err != nil || length != "$"+strconv.Itoa(len(role)) {
		return errors.New("could not verify Valkey endpoint role")
	}
	switch role {
	case "master":
		return nil
	case "slave":
		return errors.New("Valkey endpoint is a replica. Use the writable primary endpoint")
	default:
		return errors.New("Valkey endpoint must be a writable primary")
	}
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
	if err != nil {
		conn.Close()
		if e.tlsConfig != nil && len(e.tlsConfig.Certificates) == 0 {
			return nil, nil, errors.New("Valkey connection failed during authentication (does the server require a client certificate?)")
		}
		return nil, nil, errors.New("Valkey connection failed during authentication")
	}
	if reply != "+OK" {
		conn.Close()
		return nil, nil, errors.New("Valkey authentication failed")
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, nil, errors.New("could not clear the Valkey connection deadline")
	}
	return conn, reader, nil
}

// Return the first line of the RESP reply. Server responses may contain
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
	return readRESPLine(reader)
}

func readRESPLine(reader *bufio.Reader) (string, error) {
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
