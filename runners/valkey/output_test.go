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

func TestMemtierLogWriterDetectsErrorReplyAfterLogLimit(t *testing.T) {
	var logs strings.Builder
	writer := memtierLogWriter{
		destination:   &logs,
		redact:        memtierLogRedactor(config{}),
		budget:        &memtierLogBudget{remaining: 1},
		watchFailures: true,
	}
	_, _ = writer.Write([]byte("ordinary Memtier diagnostic\n"))
	if writer.failure != "" {
		t.Fatal("ordinary stderr output was treated as a Valkey error reply")
	}
	_, _ = writer.Write([]byte("server 1 handle error"))
	_, _ = writer.Write([]byte(" response: -OOM command not allowed"))
	if writer.failure != "" {
		t.Fatal("partial line was treated as a complete diagnostic")
	}
	writer.finish()
	_, _ = writer.Write([]byte("database selection failed.\n"))
	if loggerFailure := writer.failure; loggerFailure != "Valkey returned an error reply (OOM)" {
		t.Fatalf("expected the first failure after the log limit, got %q", loggerFailure)
	}
	if got := logs.String(); got != memtierLogLimitNotice {
		t.Fatalf("expected only the log limit notice, got %q", got)
	}
}

func TestMemtierFailure(t *testing.T) {
	for _, tc := range []struct {
		line string
		want string
	}{
		{"server 1 handle error response: -OOM memory limit reached", "Valkey returned an error reply (OOM)"},
		{"server 1 handle error response: -READONLY", "Valkey returned an error reply (READONLY)"},
		{"server 1 handle error response: -NOPERM private-server-detail", "Valkey returned an error reply (NOPERM)"},
		{"server 1 handle error response: -PRIVATE-detail", "Valkey returned an error reply"},
		{"error: authentication failed [-WRONGPASS private-server-detail]", "authentication failed"},
		{"database selection failed.", "database selection failed"},
		{"warning: server does not support HELLO", ""},
	} {
		if got := memtierFailure([]byte(tc.line)); got != tc.want {
			t.Errorf("memtierFailure(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}
