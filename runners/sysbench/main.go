package main

import (
	"fmt"
	"strconv"
	"strings"
)

type config struct {
	host, database, user, password             string
	port, duration, threads, tables, tableSize int
	initialize                                 bool
}

// loadConfig validates the runner's environment without opening a connection.
// Connection values are retained verbatim, including password whitespace.
func loadConfig(getenv func(string) string) (config, error) {
	var c config
	for _, field := range []struct {
		name   string
		target *string
	}{
		{"MYSQL_HOST", &c.host},
		{"MYSQL_DATABASE", &c.database},
		{"MYSQL_USER", &c.user},
		{"MYSQL_PASSWORD", &c.password},
	} {
		value := getenv(field.name)
		if value == "" || (field.name != "MYSQL_PASSWORD" && strings.TrimSpace(value) == "") {
			return config{}, fmt.Errorf("%s is required", field.name)
		}
		if strings.ContainsRune(value, '\x00') {
			return config{}, fmt.Errorf("%s must not contain NUL bytes", field.name)
		}
		*field.target = value
	}
	if strings.Contains(c.host, ",") {
		return config{}, fmt.Errorf("MYSQL_HOST must specify one host")
	}

	for _, field := range []struct {
		name              string
		fallback, maximum int
		target            *int
	}{
		{"MYSQL_PORT", 3306, 65535, &c.port},
		{"BENCH_DURATION", 60, 2147483647, &c.duration},
		{"BENCH_THREADS", 1, 2147483647, &c.threads},
		{"BENCH_TABLES", 1, 2147483647, &c.tables},
		{"BENCH_TABLE_SIZE", 10000, 2147483647, &c.tableSize},
	} {
		value := getenv(field.name)
		if value == "" {
			*field.target = field.fallback
			continue
		}
		n, err := strconv.ParseUint(value, 10, 31)
		if err != nil || n == 0 || n > uint64(field.maximum) {
			return config{}, fmt.Errorf("%s must be an integer between 1 and %d", field.name, field.maximum)
		}
		*field.target = int(n)
	}

	switch getenv("BENCH_INITIALIZE") {
	case "", "false":
	case "true":
		c.initialize = true
	default:
		return config{}, fmt.Errorf("BENCH_INITIALIZE must be true or false")
	}
	return c, nil
}
