package main

import (
	"errors"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
)

const maxOutputLineBytes = 64 * 1024

var (
	mongoURLPattern  = regexp.MustCompile(`mongodb(?:\+srv)?://[^\s"']+`)
	operationPattern = regexp.MustCompile(`^([A-Z_]+)\s+-`)
	countPattern     = regexp.MustCompile(`(?:^|,\s*)Count:\s*([0-9]+)(?:,|$)`)
)

// phaseOutput serializes both streams, bounds incomplete lines and retains only
// the observations needed to decide whether the workload itself succeeded.
type phaseOutput struct {
	mu                                 sync.Mutex
	redact                             *strings.Replacer
	load                               bool
	expected                           int
	operationFailed, invalid, finished bool
	insertCount                        uint64
	haveInsert                         bool
	writeErr                           error
}

type phaseLog struct {
	phase                  *phaseOutput
	destination            io.Writer
	line                   []byte
	properties, discarding bool
	stdout                 bool
}

func newPhaseOutput(c config, load bool, stdout, stderr io.Writer) (*phaseOutput, *phaseLog, *phaseLog) {
	secrets := []string{c.uri}
	if decoded, err := url.PathUnescape(c.uri); err == nil {
		secrets = append(secrets, decoded)
	}
	if cs, err := connstring.ParseAndValidate(c.uri); err == nil && cs.PasswordSet {
		secrets = append(secrets, cs.Password, url.QueryEscape(cs.Password), url.PathEscape(cs.Password))
		// A decoded password may itself contain line breaks in an upstream error.
		secrets = append(secrets, strings.FieldsFunc(cs.Password, func(r rune) bool { return r == '\n' || r == '\r' })...)
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	var pairs []string
	for _, secret := range secrets {
		if secret != "" {
			pairs = append(pairs, secret, "[REDACTED]")
		}
	}
	p := &phaseOutput{redact: strings.NewReplacer(pairs...), load: load, expected: c.recordCount}
	return p, &phaseLog{phase: p, destination: stdout, stdout: true}, &phaseLog{phase: p, destination: stderr}
}

// Write consumes fragments; a line is inspected and sanitized before forwarding.
func (s *phaseLog) Write(data []byte) (int, error) {
	s.phase.mu.Lock()
	defer s.phase.mu.Unlock()
	for _, b := range data {
		if b == '\n' {
			s.flushLine()
			continue
		}
		if s.discarding {
			continue
		}
		if len(s.line) == maxOutputLineBytes {
			s.line = nil
			s.discarding = true
			s.phase.invalid = true
			continue
		}
		s.line = append(s.line, b)
	}
	// Retain destination errors while draining the child's remaining output.
	return len(data), nil
}

func (s *phaseLog) flushLine() {
	defer func() { s.line = s.line[:0]; s.discarding = false }()
	if s.discarding {
		return
	}
	line := strings.TrimSuffix(string(s.line), "\r")
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "***") && strings.Contains(trimmed, "properties") {
		s.properties = true
		return
	}
	if s.properties {
		if len(trimmed) > 0 && strings.Trim(trimmed, "*") == "" {
			s.properties = false
		}
		return
	}
	s.phase.observe(trimmed, s.stdout)
	// Replace complete known secrets first: the generic URI pattern can stop at
	// a quote inside a password and otherwise leave a credential suffix behind.
	line = s.phase.redact.Replace(line)
	line = mongoURLPattern.ReplaceAllString(line, "[REDACTED URI]")
	if s.phase.writeErr == nil {
		if n, err := io.WriteString(s.destination, line+"\n"); err != nil {
			s.phase.writeErr = err
		} else if n != len(line)+1 {
			s.phase.writeErr = io.ErrShortWrite
		}
	}
}

func (p *phaseOutput) observe(line string, stdout bool) {
	if stdout && strings.HasPrefix(line, "Run finished, takes ") {
		if p.finished {
			p.invalid = true
		}
		p.finished = true
		return
	}
	operation := operationPattern.FindStringSubmatch(line)
	if operation == nil {
		return
	}
	isError := strings.HasSuffix(operation[1], "_ERROR")
	isLoadInsert := stdout && p.load && p.finished && operation[1] == "INSERT"
	if !isError && !isLoadInsert {
		return
	}
	match := countPattern.FindStringSubmatch(line)
	if match == nil {
		p.invalid = true
		return
	}
	count, err := strconv.ParseUint(match[1], 10, 64)
	if err != nil {
		p.invalid = true
		return
	}
	if isError && count > 0 {
		p.operationFailed = true
	}
	if isLoadInsert {
		if p.haveInsert {
			p.invalid = true
		}
		p.haveInsert, p.insertCount = true, count
	}
}

// finish runs after invoke has drained both streams, including final partial lines.
func (p *phaseOutput) finish(stdout, stderr *phaseLog) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, stream := range []*phaseLog{stdout, stderr} {
		if len(stream.line) > 0 || stream.discarding {
			stream.flushLine()
		}
	}
	if p.writeErr != nil {
		return errors.New("could not forward runner output")
	}
	if p.operationFailed {
		return errors.New("go-ycsb reported failed operations")
	}
	if p.invalid {
		return errors.New("go-ycsb output could not be verified")
	}
	if p.load && (!p.finished || !p.haveInsert || p.insertCount != uint64(p.expected)) {
		return errors.New("go-ycsb did not confirm loading the requested record count")
	}
	return nil
}
