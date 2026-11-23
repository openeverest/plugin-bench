package main

import (
	"strings"
	"testing"
)

func TestMemtierLogWriterRedactsAcrossWrites(t *testing.T) {
	var logs strings.Builder
	writer := memtierLogWriter{
		destination: &logs,
		redact:      memtierLogRedactor(config{username: "bench-user", password: `bench-user"secret`}),
		budget:      &memtierLogBudget{remaining: maxMemtierLogBytes},
	}
	for _, chunk := range []string{"progress\r", "\nerror for bench-user", "\"secret\n", `quoted bench-user\"secret`} {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	writer.finish()
	if got, want := logs.String(), "progress\nerror for [REDACTED]\nquoted [REDACTED]\n"; got != want {
		t.Fatalf("output was not framed and redacted as expected")
	}
}

func TestMemtierLogWriterOmitsOversizedLines(t *testing.T) {
	var logs strings.Builder
	writer := memtierLogWriter{
		destination: &logs, redact: memtierLogRedactor(config{}),
		budget: &memtierLogBudget{remaining: maxMemtierLogBytes},
	}
	_, _ = writer.Write([]byte(strings.Repeat("x", maxMemtierLogLineBytes+1) + "\rnext"))
	writer.finish()
	if got, want := logs.String(), "[Memtier output line omitted]\nnext\n"; got != want {
		t.Fatalf("output was not bounded or did not resume after the oversized line")
	}
}

func TestMemtierLogBudgetSpansStreamsAndPhases(t *testing.T) {
	var stdout, stderr strings.Builder
	redact := memtierLogRedactor(config{password: "secret"})
	budget := &memtierLogBudget{remaining: len("[REDACTED]\nwarn\n")}
	for _, output := range []struct {
		destination *strings.Builder
		text        string
	}{
		{&stdout, "secret\n"},
		{&stderr, "warn\n"},
		{&stdout, "omitted\nmore\n"},
		{&stderr, "also omitted"},
	} {
		writer := memtierLogWriter{destination: output.destination, redact: redact, budget: budget}
		if n, err := writer.Write([]byte(output.text)); n != len(output.text) || err != nil {
			t.Fatalf("expected writer to drain output, got %d bytes and error %v", n, err)
		}
		writer.finish()
	}
	if stdout.String() != "[REDACTED]\n"+memtierLogLimitNotice || stderr.String() != "warn\n" {
		t.Fatalf("expected one notice and no further Memtier output, got stdout %q and stderr %q", &stdout, &stderr)
	}
}
