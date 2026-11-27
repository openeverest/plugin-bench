package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunStopsMemtierOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name, seed, measure, want string
		verifySeed                bool
	}{
		{
			name:    "seeding error",
			seed:    "echo 'server 1 handle error response: -OOM limit reached' >&2; exec sleep 60",
			measure: "exit 99",
			want:    "seeding failed: Valkey returned an error reply (OOM)",
		},
		{
			name:       "measurement error",
			seed:       "exit 0",
			measure:    "echo 'database selection failed.' >&2; exec sleep 60",
			want:       "measurement failed: database selection failed",
			verifySeed: true,
		},
		{
			name:       "final diagnostic followed by exit zero",
			seed:       "exit 0",
			measure:    "printf 'error: authentication failed [-WRONGPASS rejected]' >&2",
			want:       "measurement failed: authentication failed",
			verifySeed: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			executable := filepath.Join(dir, "memtier")
			script := "#!/bin/sh\nfor arg do\n case \"$arg\" in\n" +
				"  --ratio=1:0) " + tc.seed + " ;;\n" +
				"  --ratio=1:9) " + tc.measure + " ;;\n esac\ndone\n"
			if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan error, 1)
			go func() { done <- serveRunnerFailureTest(listener, tc.verifySeed) }()

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err = run(ctx, config{
				host: "127.0.0.1", port: listener.Addr().(*net.TCPAddr).Port,
				username: "user", password: "password", keyCount: 1,
			}, executable, privateFiles{resultPath: filepath.Join(dir, "results.json")})
			listener.Close()
			if serverErr := <-done; serverErr != nil {
				t.Fatal(serverErr)
			}
			if err == nil || err.Error() != tc.want || ctx.Err() != nil {
				t.Fatalf("expected %q before the deadline, got %v (context error %v)", tc.want, err, ctx.Err())
			}
		})
	}
}

func serveRunnerFailureTest(listener *net.TCPListener, verify bool) error {
	const clusterInfo = "cluster_enabled:0\r\n"
	err := runRESPExchanges(listener, []respExchange{
		{testAuthRequest, "+OK\r\n"},
		{"*2\r\n$4\r\nINFO\r\n$7\r\ncluster\r\n", fmt.Sprintf("$%d\r\n%s\r\n", len(clusterInfo), clusterInfo)},
		{"*1\r\n$4\r\nROLE\r\n", "*1\r\n$6\r\nmaster\r\n"},
	})
	if err != nil {
		return err
	}
	if verify {
		if err := serveRunnerKeyCommand(listener, "EXISTS"); err != nil {
			return err
		}
	}
	return serveRunnerKeyCommand(listener, "UNLINK")
}

func serveRunnerKeyCommand(listener *net.TCPListener, command string) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	auth := make([]byte, len(testAuthRequest))
	if _, err := io.ReadFull(conn, auth); err != nil {
		return err
	}
	if string(auth) != testAuthRequest {
		return fmt.Errorf("expected AUTH before %s", command)
	}
	if _, err := io.WriteString(conn, "+OK\r\n"); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	for _, want := range []string{"*2", "$6", command} {
		line, err := readRESPLine(reader)
		if err != nil || line != want {
			return fmt.Errorf("expected %s request line %q, got %q (%v)", command, want, line, err)
		}
	}
	if _, err := readRESPLine(reader); err != nil {
		return err
	}
	key, err := readRESPLine(reader)
	if err != nil || !strings.HasPrefix(key, "plugin-bench:") || !strings.HasSuffix(key, ":1") {
		return fmt.Errorf("expected %s on the benchmark key, got %q (%v)", command, key, err)
	}
	_, err = io.WriteString(conn, ":1\r\n")
	return err
}
