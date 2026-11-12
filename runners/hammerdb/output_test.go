package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestOutputRedactsFragmentsAndEncodedSettings(t *testing.T) {
	c := executionConfig(t)
	c.password = "secret$[x]\\'value\nsecondpart"
	literal := tclLiteral(c.password)
	variants := []string{c.password, literal[1 : len(literal)-1], strings.ReplaceAll(strings.ReplaceAll(c.password, `\`, `\\`), `'`, `\'`)}
	for _, value := range variants {
		var destination bytes.Buffer
		out, diagnostic := newOutput(c, &destination, &destination)
		for _, b := range []byte("password=" + value + "\nfinal=" + value) {
			out.Write([]byte{b})
		}
		if err := finishOutput(out, diagnostic); err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{value, "secret$[x]", "secondpart"} {
			if strings.Contains(destination.String(), secret) {
				t.Fatal("credential leaked")
			}
		}
		if !strings.Contains(destination.String(), "[REDACTED]") {
			t.Fatal("missing redaction")
		}
	}
}

func TestOutputConcurrentAndOversized(t *testing.T) {
	c := executionConfig(t)
	var destination bytes.Buffer
	out, diagnostic := newOutput(c, &destination, &destination)
	var wg sync.WaitGroup
	for _, stream := range []*outputStream{out, diagnostic} {
		wg.Add(1)
		go func(s *outputStream) {
			defer wg.Done()
			s.Write([]byte(strings.Repeat("x", maxOutputLineBytes) + c.password + "\n"))
			s.Write([]byte("normal output\n"))
		}(stream)
	}
	wg.Wait()
	if err := finishOutput(out, diagnostic); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(destination.String(), c.password) || strings.Contains(destination.String(), "xxx") {
		t.Fatal("oversized output was forwarded")
	}
	if strings.Count(destination.String(), "normal output") != 2 {
		t.Fatal("lost following output")
	}
}

type failedOutput struct{ short bool }

func (w failedOutput) Write(p []byte) (int, error) {
	if w.short {
		return 0, nil
	}
	return 0, errors.New("sensitive destination error")
}

func TestOutputRetainsWriteFailuresWhileDraining(t *testing.T) {
	for _, short := range []bool{false, true} {
		c := executionConfig(t)
		out, diagnostic := newOutput(c, failedOutput{short}, io.Discard)
		data := []byte("first\nsecond\npartial")
		if n, err := out.Write(data); n != len(data) || err != nil {
			t.Fatal("stopped draining child")
		}
		if err := finishOutput(out, diagnostic); err == nil || strings.Contains(err.Error(), "sensitive") {
			t.Fatal("expected safe output failure")
		}
	}
}

func TestStagedOutputRedactionAndCleanup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	executable := filepath.Join(t.TempDir(), "hammerdbcli")
	// This stand-in echoes the full generated script as a hostile diagnostic.
	// It verifies the Go lifecycle, not HammerDB's database behavior.
	script := `#!/bin/sh
cat "$2"
cat "$2" >&2
phase=$(basename "$2" .tcl)
umask 077
printf '1 %s JOB123\n' "$phase" > "$(dirname "$2")/$phase.done"
`
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c := executionConfig(t)
	c.initialize = true
	c.password = "dummy$[secret]\\value"
	var output bytes.Buffer
	if err := runStaged(context.Background(), c, executable, os.Environ(), &output, &output); err != nil {
		t.Fatal(err)
	}
	literal := tclLiteral(c.password)
	if strings.Contains(output.String(), c.password) || strings.Contains(output.String(), literal[1:len(literal)-1]) {
		t.Fatal("script echo leaked password")
	}
	if !strings.Contains(output.String(), "[REDACTED]") {
		t.Fatal("did not exercise redaction")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("private scripts were not removed")
	}
}
