package main

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxSeedPayloadBytes = 64 * 1024 * 1024
	maxTotalClients     = 128
)

type config struct {
	host, username, password, tlsCA string
	port                            int
	tls                             bool
	durationSeconds                 int
	threads                         int
	clientsPerThread                int
	keyCount                        int
	valueSizeBytes                  int
}

// loadConfig validates all runner environment settings without connecting to
// Valkey. Errors identify the setting but never include its value.
func loadConfig(getenv func(string) string) (config, error) {
	var c config

	c.host = getenv("VALKEY_HOST")
	if !validHost(c.host) {
		return config{}, errors.New("VALKEY_HOST must be an IP address or DNS hostname without a port")
	}
	value := getenv("VALKEY_PORT")
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil || port == 0 {
		return config{}, errors.New("VALKEY_PORT must be an integer between 1 and 65535")
	}
	c.port = int(port)

	c.username = getenv("VALKEY_USERNAME")
	c.password = getenv("VALKEY_PASSWORD")
	if c.username == "" || strings.ContainsFunc(c.username, unicode.IsControl) || !utf8.ValidString(c.username) {
		return config{}, errors.New("VALKEY_USERNAME is required and must not contain control characters")
	}
	if strings.ContainsRune(c.username, ':') {
		return config{}, errors.New("VALKEY_USERNAME must not contain a colon because Memtier uses it to separate credentials")
	}
	if c.password == "" || strings.ContainsFunc(c.password, unicode.IsControl) || !utf8.ValidString(c.password) {
		return config{}, errors.New("VALKEY_PASSWORD is required and must not contain control characters")
	}

	switch getenv("VALKEY_TLS") {
	case "", "true":
		c.tls = true
	case "false":
		c.tls = false
	default:
		return config{}, errors.New("VALKEY_TLS must be true or false")
	}
	c.tlsCA = getenv("VALKEY_TLS_CA")
	if c.tls {
		if c.tlsCA == "" || !x509.NewCertPool().AppendCertsFromPEM([]byte(c.tlsCA)) {
			return config{}, errors.New("VALKEY_TLS_CA must contain a PEM-encoded CA certificate when VALKEY_TLS is true")
		}
	} else if c.tlsCA != "" {
		return config{}, errors.New("VALKEY_TLS_CA requires VALKEY_TLS=true")
	}

	for _, field := range []struct {
		name             string
		initial, maximum int
		target           *int
	}{
		{"BENCH_DURATION", 30, 300, &c.durationSeconds},
		{"BENCH_THREADS", 2, maxTotalClients, &c.threads},
		{"BENCH_CLIENTS_PER_THREAD", 10, maxTotalClients, &c.clientsPerThread},
		{"BENCH_KEY_COUNT", 10_000, 100_000, &c.keyCount},
		{"BENCH_VALUE_SIZE", 256, 16_384, &c.valueSizeBytes},
	} {
		value := getenv(field.name)
		if value == "" {
			*field.target = field.initial
			continue
		}
		n, err := strconv.ParseUint(value, 10, 31)
		if err != nil || n == 0 || n > uint64(field.maximum) {
			return config{}, fmt.Errorf("%s must be an integer between 1 and %d", field.name, field.maximum)
		}
		*field.target = int(n)
	}

	if c.threads > maxTotalClients/c.clientsPerThread {
		return config{}, fmt.Errorf("BENCH_THREADS times BENCH_CLIENTS_PER_THREAD must not exceed %d total clients", maxTotalClients)
	}
	if c.keyCount > maxSeedPayloadBytes/c.valueSizeBytes {
		return config{}, fmt.Errorf("BENCH_KEY_COUNT times BENCH_VALUE_SIZE must not exceed %d bytes", maxSeedPayloadBytes)
	}
	return c, nil
}

func validHost(host string) bool {
	if host == "" || strings.ContainsFunc(host, unicode.IsControl) || strings.ContainsAny(host, "[]/\\") || strings.ContainsFunc(host, unicode.IsSpace) {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if strings.ContainsRune(host, ':') {
		return false // Ports are supplied separately; malformed IPv6 is not a hostname.
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}
