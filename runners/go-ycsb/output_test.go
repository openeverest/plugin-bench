package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestPhaseOutputFixtures(t *testing.T) {
	for _, tc := range []struct {
		name string
		load bool
		fail bool
	}{
		{"load", true, false}, {"run", false, false}, {"duplicate-load", true, true}, {"missing-data", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + tc.name + "-v1.0.3.txt")
			if err != nil {
				t.Fatal(err)
			}
			c := config{uri: validEnvironment()["MONGODB_URI"], recordCount: 1000}
			var out, stderr strings.Builder
			p, a, b := newPhaseOutput(c, tc.load, &out, &stderr)
			for _, v := range data {
				a.Write([]byte{v})
			}
			err = p.finish(a, b)
			if (err != nil) != tc.fail {
				t.Fatalf("unexpected workload result: %v", err)
			}
			if strings.Contains(out.String(), "properties") || strings.Contains(out.String(), "mongodb.url") {
				t.Fatal("properties banner leaked")
			}
			if !strings.Contains(out.String(), "Run finished") {
				t.Fatal("useful logs lost")
			}
		})
	}
}

func TestPhaseOutputRedactsBothStreams(t *testing.T) {
	c := config{uri: "mongodb://user:p%40ss%23word@localhost/ycsb?authSource=admin"}
	for _, text := range []string{c.uri, "password p@ss#word", "password p%40ss%23word", "mongodb://other:credential@other-host/ycsb"} {
		var out, stderr strings.Builder
		p, a, b := newPhaseOutput(c, false, &out, &stderr)
		for _, w := range []io.Writer{a, b} {
			for _, v := range []byte(text) {
				w.Write([]byte{v})
			}
		}
		if err := p.finish(a, b); err != nil {
			t.Fatal(err)
		}
		for _, logs := range []string{out.String(), stderr.String()} {
			for _, secret := range []string{c.uri, "p@ss#word", "p%40ss%23word", "other:credential"} {
				if strings.Contains(logs, secret) {
					t.Fatal("secret leaked")
				}
			}
			if !strings.Contains(logs, "REDACTED") {
				t.Fatal("redaction missing")
			}
		}
	}
}

func TestPhaseOutputRedactsQuotedPasswords(t *testing.T) {
	for _, password := range []string{`prefix'SECRET_SUFFIX`, `prefix"SECRET_SUFFIX`} {
		c, err := configFromEnvironment(map[string]string{
			"MONGODB_URI": "mongodb://user:" + password + "@localhost/ycsb",
		})
		if err != nil {
			t.Fatal(err)
		}
		var stdout, stderr strings.Builder
		p, a, b := newPhaseOutput(c, false, &stdout, &stderr)
		for _, stream := range []io.Writer{a, b} {
			for _, line := range []string{c.uri, "password: " + password} {
				for _, value := range []byte(line + "\n") {
					stream.Write([]byte{value})
				}
			}
		}
		if err := p.finish(a, b); err != nil {
			t.Fatal(err)
		}
		for _, logs := range []string{stdout.String(), stderr.String()} {
			if strings.Contains(logs, "prefix") || strings.Contains(logs, "SECRET_SUFFIX") || !strings.Contains(logs, "REDACTED") {
				t.Fatalf("quoted credentials were not completely redacted: %q", logs)
			}
		}
	}
}

func TestPhaseOutputVerification(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		load, fail bool
	}{
		{"incomplete", "Run finished, takes 1s\nINSERT - Takes(s): 1, Count: 19, OPS: 1", true, true},
		{"no final marker", "INSERT - Takes(s): 1, Count: 20, OPS: 1\n", true, true},
		{"missing insert", "Run finished, takes 1s\n", true, true},
		{"periodic error", "READ_ERROR - Takes(s): 1, Count: 1, OPS: 1\nRun finished, takes 2s\n", false, true},
		{"zero errors", "READ_ERROR - Takes(s): 1, Count: 0, OPS: 0", false, false},
		{"bad count", "UPDATE_ERROR - Takes(s): 1, Count: invalid, OPS: 1", false, true},
		{"overflow count", "INSERT_ERROR - Takes(s): 1, Count: 999999999999999999999, OPS: 1", false, true},
		{"duplicate final", "Run finished, takes 1s\nINSERT - Takes(s): 1, Count: 20, OPS: 1\nINSERT - Takes(s): 1, Count: 20, OPS: 1", true, true},
		{"missing optional metrics", "benchmark completed\n", false, false},
		{"oversized", strings.Repeat("x", maxOutputLineBytes+1), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, a, b := newPhaseOutput(config{recordCount: 20}, tc.load, io.Discard, io.Discard)
			a.Write([]byte(tc.text))
			if err := p.finish(a, b); (err != nil) != tc.fail {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
}

func TestPhaseOutputConcurrentStreams(t *testing.T) {
	var out strings.Builder
	p, a, b := newPhaseOutput(config{}, false, &out, &out)
	var wg sync.WaitGroup
	for _, w := range []io.Writer{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				io.WriteString(w, "safe line\n")
			}
		}()
	}
	wg.Wait()
	if err := p.finish(a, b); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "safe line") != 200 {
		t.Fatal("lost output")
	}
}

type failedLogWriter struct{}

func (failedLogWriter) Write([]byte) (int, error) { return 0, errors.New("private destination error") }
func TestPhaseOutputDestinationFailure(t *testing.T) {
	p, a, b := newPhaseOutput(config{}, false, failedLogWriter{}, io.Discard)
	a.Write([]byte("safe\n"))
	if err := p.finish(a, b); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("destination failures need safe errors")
	}
}

func TestRunRejectsZeroExitLoadErrors(t *testing.T) {
	for _, text := range []string{"INSERT_ERROR - Takes(s): 1, Count: 1, OPS: 1", "Run finished, takes 1s\nINSERT - Takes(s): 1, Count: 3, OPS: 1"} {
		c, _ := configFromEnvironment(validEnvironment())
		c.initialize = true
		calls := 0
		err := run(context.Background(), c, func(_ context.Context, _ []string, out, _ io.Writer) error {
			calls++
			io.WriteString(out, text)
			return nil
		}, io.Discard, io.Discard)
		if err == nil || calls != 1 {
			t.Fatal("failed or incomplete load must prevent measured run")
		}
	}
}

func TestRunChildErrorDoesNotExposeURI(t *testing.T) {
	c, _ := configFromEnvironment(validEnvironment())
	cause := errors.New("connection failed: " + c.uri)
	err := run(context.Background(), c, func(context.Context, []string, io.Writer, io.Writer) error { return cause }, io.Discard, io.Discard)
	if !errors.Is(err, cause) || strings.Contains(err.Error(), c.uri) {
		t.Fatal("preserve child cause without exposing its text")
	}
}
