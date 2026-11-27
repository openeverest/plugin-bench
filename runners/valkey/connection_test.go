package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testAuthRequest = "*3\r\n$4\r\nAUTH\r\n$4\r\nuser\r\n$8\r\npassword\r\n"

type respExchange struct {
	request string
	reply   string
}

// serveRESP expects exact request bytes and closes the connection after the
// final exchange. An empty final reply simulates a server that closes mid-command.
func serveRESP(t *testing.T, exchanges ...respExchange) valkeyEndpoint {
	t.Helper()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- runRESPExchanges(listener, exchanges)
	}()
	t.Cleanup(func() {
		listener.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return valkeyEndpoint{address: listener.Addr().String(), user: "user", password: "password"}
}

func runRESPExchanges(listener *net.TCPListener, exchanges []respExchange) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	for i, exchange := range exchanges {
		request := make([]byte, len(exchange.request))
		if _, err := io.ReadFull(conn, request); err != nil {
			return fmt.Errorf("read RESP request %d: %w", i+1, err)
		}
		if string(request) != exchange.request {
			return fmt.Errorf("unexpected RESP request %d: got %q, want %q", i+1, request, exchange.request)
		}
		if exchange.reply != "" {
			if _, err := io.WriteString(conn, exchange.reply); err != nil {
				return fmt.Errorf("write RESP reply %d: %w", i+1, err)
			}
		}
	}
	return nil
}

func TestConnectAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name, reply, wantError string
	}{
		{name: "accepted", reply: "+OK\r\n"},
		{name: "rejected", reply: "-WRONGPASS password rejected\r\n", wantError: "Valkey authentication failed"},
		{name: "connection closed before reply", wantError: "Valkey connection failed during authentication"},
		{name: "incomplete reply", reply: "+OK", wantError: "Valkey connection failed during authentication"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := serveRESP(t, respExchange{testAuthRequest, tc.reply})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			conn, reader, err := endpoint.connect(ctx)
			if conn != nil {
				defer conn.Close()
			}
			if tc.wantError == "" {
				if err != nil || conn == nil || reader == nil {
					t.Fatalf("expected an authenticated connection and reader, got error %v", err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantError || conn != nil || reader != nil {
				t.Fatalf("expected %q without a connection or reader, got %v", tc.wantError, err)
			}
		})
	}
}

func TestConnectCanceledDuringAuthentication(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	endpoint := valkeyEndpoint{address: listener.Addr().String(), user: "user", password: "password"}
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		err = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if err == nil {
			request := make([]byte, len(testAuthRequest))
			_, err = io.ReadFull(conn, request)
		}
		if err == nil {
			cancel()
			var extra [1]byte
			if _, readErr := conn.Read(extra[:]); readErr != io.EOF {
				err = fmt.Errorf("expected the canceled client to close its connection, got %v", readErr)
			}
		}
		serverDone <- err
	}()
	conn, _, err := endpoint.connect(ctx)
	if conn != nil {
		conn.Close()
	}
	listener.Close()
	if serverErr := <-serverDone; serverErr != nil {
		t.Fatal(serverErr)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestVerifySeed(t *testing.T) {
	const prefix = "plugin-bench:test:"
	const keyCount = keyBatchSize + 1
	var firstBatch strings.Builder
	fmt.Fprintf(&firstBatch, "*%d\r\n$6\r\nEXISTS\r\n", keyBatchSize+1)
	for i := 1; i <= keyBatchSize; i++ {
		key := prefix + strconv.Itoa(i)
		fmt.Fprintf(&firstBatch, "$%d\r\n%s\r\n", len(key), key)
	}
	lastKey := prefix + strconv.Itoa(keyCount)
	lastRequest := fmt.Sprintf("*2\r\n$6\r\nEXISTS\r\n$%d\r\n%s\r\n", len(lastKey), lastKey)
	for _, tc := range []struct {
		name, reply string
		wantError   bool
	}{
		{name: "all keys present", reply: ":1\r\n"},
		{name: "partial key count", reply: ":0\r\n", wantError: true},
		{name: "connection closed", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := serveRESP(t,
				respExchange{testAuthRequest, "+OK\r\n"},
				respExchange{firstBatch.String(), fmt.Sprintf(":%d\r\n", keyBatchSize)},
				respExchange{lastRequest, tc.reply},
			)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := verifySeed(ctx, endpoint, prefix, keyCount)
			if tc.wantError {
				if err == nil || err.Error() != "Valkey did not confirm that all benchmark keys were seeded" {
					t.Fatalf("expected seed verification error, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
