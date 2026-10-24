package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// Sysbench 1.0.20 reads configuration lines into a 256-byte buffer and has no
// quote escaping. Reject unsupported values instead of altering credentials.
func connectionConfig(c config) ([]byte, error) {
	var data strings.Builder
	for _, field := range []struct {
		setting, option, value string
	}{
		{"MYSQL_HOST", "mysql-host", c.host},
		{"MYSQL_PORT", "mysql-port", strconv.Itoa(c.port)},
		{"MYSQL_DATABASE", "mysql-db", c.database},
		{"MYSQL_USER", "mysql-user", c.user},
		{"MYSQL_PASSWORD", "mysql-password", c.password},
	} {
		value := field.value
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("%s cannot contain NUL bytes or line breaks", field.setting)
		}
		switch {
		case !strings.Contains(value, "'"):
			value = "'" + value + "'"
		case !strings.Contains(value, `"`):
			value = `"` + value + `"`
		case strings.ContainsRune(value, ',') || strings.ContainsFunc(value, unicode.IsSpace) || strings.ContainsAny(value[:1], "'\"#"):
			return nil, fmt.Errorf("%s cannot be represented in the sysbench configuration file", field.setting)
		default:
			// Both quote types present: sysbench reads the value unquoted.
		}
		line := field.option + "=" + value + "\n"
		if len(line) > 254 {
			return nil, fmt.Errorf("%s exceeds the sysbench configuration line limit", field.setting)
		}
		data.WriteString(line)
	}
	return []byte(data.String()), nil
}

func writeConnectionConfig(c config) (path string, cleanup func(), err error) {
	data, err := connectionConfig(c)
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "sysbench-")
	if err != nil {
		return "", nil, errors.New("could not create the private sysbench configuration directory")
	}
	path = filepath.Join(dir, "connection.conf")
	cleanup = func() {
		_ = os.Remove(path)
		_ = os.Remove(dir)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		cleanup()
		return "", nil, errors.New("could not create the private sysbench configuration file")
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		cleanup()
		return "", nil, errors.New("could not write the private sysbench configuration file")
	}
	return path, cleanup, nil
}
