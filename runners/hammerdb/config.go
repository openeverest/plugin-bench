package main

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type config struct {
	databaseType, workload         string
	host, database, user, password string
	port                           int
	warehouses, virtualUsers       int
	rampupMinutes, durationMinutes int
	initialize                     bool
	pgSSLMode                      string
	mysqlSSL                       bool
}

// loadConfig validates settings without contacting the database or HammerDB.
// Errors identify settings without including connection values or passwords.
func loadConfig(getenv func(string) string) (config, error) {
	c := config{databaseType: getenv("DB_TYPE"), workload: getenv("BENCH_WORKLOAD")}
	switch c.databaseType {
	case "postgresql":
		c.port = 5432
	case "mysql":
		c.port = 3306
	default:
		return config{}, errors.New("DB_TYPE must be postgresql or mysql")
	}
	if c.workload == "" {
		c.workload = "tproc-c"
	}
	if c.workload != "tproc-c" {
		return config{}, errors.New("BENCH_WORKLOAD must be tproc-c")
	}
	for _, field := range []struct {
		name   string
		target *string
	}{
		{"DB_HOST", &c.host},
		{"DB_NAME", &c.database},
		{"DB_USER", &c.user},
		{"DB_PASSWORD", &c.password},
	} {
		value := getenv(field.name)
		if value == "" {
			return config{}, fmt.Errorf("%s is required", field.name)
		}
		if strings.ContainsRune(value, '\x00') {
			return config{}, fmt.Errorf("%s must not contain NUL bytes", field.name)
		}
		*field.target = value
	}
	if err := validatePassword(c.password); err != nil {
		return config{}, err
	}
	if !validHost(c.host) {
		return config{}, errors.New("DB_HOST must be one IP address or DNS hostname without a port")
	}
	databaseLimit, userLimit := 63, 63
	if c.databaseType == "mysql" {
		databaseLimit, userLimit = 64, 32
	}
	if err := validateName("DB_NAME", c.database, databaseLimit); err != nil {
		return config{}, err
	}
	if err := validateName("DB_USER", c.user, userLimit); err != nil {
		return config{}, err
	}

	for _, field := range []struct {
		name                       string
		fallback, minimum, maximum int
		target                     *int
	}{
		{"DB_PORT", c.port, 1, 65535, &c.port},
		{"BENCH_WAREHOUSES", 1, 1, 2147483647, &c.warehouses},
		{"BENCH_VIRTUAL_USERS", 1, 1, 2147483647, &c.virtualUsers},
		{"BENCH_RAMPUP_MINUTES", 0, 0, 2147483647, &c.rampupMinutes},
		{"BENCH_DURATION_MINUTES", 1, 1, 2147483647, &c.durationMinutes},
	} {
		value := getenv(field.name)
		if value == "" {
			*field.target = field.fallback
			continue
		}
		n, err := strconv.ParseUint(value, 10, 31)
		if err != nil || n < uint64(field.minimum) || n > uint64(field.maximum) {
			return config{}, fmt.Errorf("%s must be an integer between %d and %d", field.name, field.minimum, field.maximum)
		}
		*field.target = int(n)
	}
	if c.virtualUsers > c.warehouses {
		return config{}, errors.New("BENCH_VIRTUAL_USERS must not exceed BENCH_WAREHOUSES")
	}
	// Check before a later conversion to time.Duration can overflow. Preparation
	// and startup require additional budget in the backend integration.
	const maximumMinutes = int64((1<<63 - 1) / time.Minute)
	if int64(c.rampupMinutes)+int64(c.durationMinutes) > maximumMinutes {
		return config{}, errors.New("BENCH_RAMPUP_MINUTES plus BENCH_DURATION_MINUTES exceeds the supported time budget")
	}
	var err error
	if c.initialize, err = boolSetting(getenv, "BENCH_INITIALIZE", false); err != nil {
		return config{}, err
	}
	if c.databaseType == "postgresql" {
		c.pgSSLMode = getenv("PGSSLMODE")
		if c.pgSSLMode == "" {
			c.pgSSLMode = "prefer"
		}
		switch c.pgSSLMode {
		case "disable", "prefer", "require":
		default:
			return config{}, errors.New("PGSSLMODE must be disable, prefer or require")
		}
	} else if c.mysqlSSL, err = boolSetting(getenv, "MYSQL_SSL", true); err != nil {
		return config{}, err
	}
	return c, nil
}

// HammerDB 6.1.1's diset uses concat, which trims surrounding whitespace.
// Its worker templates quote passwords again without escaping double quotes.
// Reject those forms rather than changing credentials or emitting broken Tcl.
func validatePassword(value string) error {
	if value == "" {
		return errors.New("DB_PASSWORD is required")
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return errors.New("DB_PASSWORD must be valid UTF-8 without NUL bytes")
	}
	if value != strings.TrimSpace(value) {
		return errors.New("DB_PASSWORD cannot have leading or trailing whitespace with HammerDB 6.1.1")
	}
	if strings.ContainsRune(value, '"') {
		return errors.New("DB_PASSWORD cannot contain double quotes with HammerDB 6.1.1")
	}
	return nil
}

func boolSetting(getenv func(string) string, name string, fallback bool) (bool, error) {
	switch getenv(name) {
	case "":
		return fallback, nil
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be true or false", name)
	}
}

// The pinned builders interpolate database/user names into generated Tcl and
// SQL. Initially accept portable names, including OpenEverest's hyphenated
// usernames, rather than passing SQL delimiters through those templates.
func validateName(name, value string, maximum int) error {
	if len(value) < 1 || len(value) > maximum {
		return fmt.Errorf("%s must contain between 1 and %d ASCII characters", name, maximum)
	}
	for _, ch := range value {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' || ch == '.' {
			continue
		}
		return fmt.Errorf("%s must contain only ASCII letters, digits, underscores, hyphens or dots", name)
	}
	return nil
}

func validHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
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
