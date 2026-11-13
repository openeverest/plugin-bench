package main

import (
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
)

const maxOutputLineBytes = 64 * 1024

// outputFilter serializes destinations and retains errors while draining both
// child pipes. Whole-line buffering prevents secrets split across writes leaking.
type outputFilter struct {
	mu     sync.Mutex
	redact *strings.Replacer
	err    error
}

type outputStream struct {
	filter      *outputFilter
	destination io.Writer
	line        []byte
	discarding  bool
}

func newOutput(c config, stdout, stderr io.Writer) (*outputStream, *outputStream) {
	// Include Tcl's encoded representation as well as the original password:
	// HammerDB can echo generated settings or connection diagnostics.
	literal := tclLiteral(c.password)
	secrets := []string{c.password, literal[1 : len(literal)-1],
		hammerdbPassword(c.password),
		strings.ReplaceAll(hammerdbPassword(c.password), `\`, `\\`),
		strings.ReplaceAll(strings.ReplaceAll(c.password, `\`, `\\`), `'`, `\'`)}
	// An upstream diagnostic may render embedded line breaks as separate lines.
	for _, representation := range append([]string(nil), secrets...) {
		for _, fragment := range strings.FieldsFunc(representation, func(r rune) bool { return r == '\r' || r == '\n' }) {
			if len(fragment) >= 4 {
				secrets = append(secrets, fragment)
			}
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	var pairs []string
	for _, secret := range secrets {
		if secret != "" {
			pairs = append(pairs, secret, "[REDACTED]")
		}
	}
	filter := &outputFilter{redact: strings.NewReplacer(pairs...)}
	return &outputStream{filter: filter, destination: stdout}, &outputStream{filter: filter, destination: stderr}
}

func (s *outputStream) Write(data []byte) (int, error) {
	s.filter.mu.Lock()
	defer s.filter.mu.Unlock()
	for _, b := range data {
		if b == '\n' {
			s.flush(true)
			continue
		}
		if s.discarding {
			continue
		}
		if len(s.line) == maxOutputLineBytes {
			s.line = s.line[:0]
			s.discarding = true
			continue
		}
		s.line = append(s.line, b)
	}
	return len(data), nil
}

func (s *outputStream) flush(newline bool) {
	text := "[runner output line omitted]"
	if !s.discarding {
		text = s.filter.redact.Replace(string(s.line))
	}
	if newline {
		text += "\n"
	}
	if s.filter.err == nil {
		n, err := io.WriteString(s.destination, text)
		if err != nil {
			s.filter.err = err
		} else if n != len(text) {
			s.filter.err = io.ErrShortWrite
		}
	}
	s.line = s.line[:0]
	s.discarding = false
}

// finish is called only after execute has drained both child streams.
func finishOutput(stdout, stderr *outputStream) error {
	stdout.filter.mu.Lock()
	defer stdout.filter.mu.Unlock()
	for _, stream := range []*outputStream{stdout, stderr} {
		if len(stream.line) > 0 || stream.discarding {
			stream.flush(false)
		}
	}
	if stdout.filter.err != nil {
		return errors.New("could not forward HammerDB output")
	}
	return nil
}
