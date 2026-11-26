package main

import (
	"bytes"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
)

const (
	maxMemtierLogLineBytes  = 64 * 1024
	maxMemtierLogBytes      = 768 * 1024
	memtierLogLimitNotice   = "[Further Memtier output omitted]\n"
	memtierErrorReplyMarker = " handle error response: "
)

// One budget covers both streams and phases, leaving room for runner diagnostics.
type memtierLogBudget struct {
	mu        sync.Mutex
	remaining int
	truncated bool
}

func (b *memtierLogBudget) writeLine(destination io.Writer, line string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.truncated {
		return nil
	}
	if len(line) > b.remaining {
		b.truncated = true
		line = memtierLogLimitNotice
	} else {
		b.remaining -= len(line)
	}
	n, err := io.WriteString(destination, line)
	if err == nil && n != len(line) {
		return io.ErrShortWrite
	}
	return err
}

func memtierLogRedactor(c config) *strings.Replacer {
	var secrets []string
	for _, value := range []string{c.username, c.password} {
		if value == "" {
			continue
		}
		quoted := strconv.Quote(value)
		secrets = append(secrets, value, quoted[1:len(quoted)-1])
	}
	// Prefer longer matches when a username or password shares a prefix.
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	var pairs []string
	for _, secret := range secrets {
		pairs = append(pairs, secret, "[REDACTED]")
	}
	return strings.NewReplacer(pairs...)
}

// Each subprocess stream has its own writer. Buffer complete lines so a
// credential split across writes is still redacted before reaching pod logs.
type memtierLogWriter struct {
	destination       io.Writer
	redact            *strings.Replacer
	budget            *memtierLogBudget
	line              []byte
	discarding        bool
	afterCR           bool
	err               error
	watchErrorReplies bool
	sawErrorReply     bool
}

func (w *memtierLogWriter) Write(data []byte) (int, error) {
	for _, b := range data {
		skipLF := w.afterCR && b == '\n'
		w.afterCR = b == '\r'
		if skipLF {
			continue
		}
		// Memtier uses carriage returns to update progress in place.
		if b == '\n' || b == '\r' {
			w.flush()
			continue
		}
		if w.discarding {
			continue
		}
		if len(w.line) == maxMemtierLogLineBytes {
			w.discarding = true
			continue
		}
		w.line = append(w.line, b)
	}
	// Keep draining the subprocess even if the log destination fails.
	return len(data), nil
}

func (w *memtierLogWriter) flush() {
	// Inspect the original line before redaction or the shared log limit.
	if w.watchErrorReplies && !w.sawErrorReply && bytes.Contains(w.line, []byte(memtierErrorReplyMarker)) {
		w.sawErrorReply = true
	}
	if w.err == nil {
		line := "[Memtier output line omitted]"
		if !w.discarding {
			line = w.redact.Replace(string(w.line))
		}
		w.err = w.budget.writeLine(w.destination, line+"\n")
	}
	w.line = w.line[:0]
	w.discarding = false
}

// finish runs after exec has drained the stream.
func (w *memtierLogWriter) finish() {
	if len(w.line) != 0 || w.discarding {
		w.flush()
	}
}
