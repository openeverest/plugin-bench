package main

import (
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testConfig(t *testing.T) config {
	t.Helper()
	env := validEnvironment()
	c, err := loadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal("could not load test configuration")
	}
	return c
}

func TestWriteConnectionConfig(t *testing.T) {
	temporaryRoot := t.TempDir()
	t.Setenv("TMPDIR", temporaryRoot)
	path, cleanup, err := writeConnectionConfig(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	for _, target := range []struct {
		path string
		mode os.FileMode
	}{{path, 0600}, {filepath.Dir(path), 0700}} {
		info, err := os.Stat(target.path)
		if err != nil || info.Mode().Perm() != target.mode {
			t.Fatal("connection configuration has incorrect permissions")
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "mysql-host='mysql'\nmysql-port='3306'\nmysql-db='sbtest'\nmysql-user='sbuser'\nmysql-password='dummy-secret'\n"
	if string(data) != want {
		t.Fatal("connection configuration did not contain the expected options")
	}
	cleanup()
	entries, err := os.ReadDir(temporaryRoot)
	if err != nil || len(entries) != 0 {
		t.Fatal("cleanup left a connection file or directory behind")
	}
}

func TestConnectionConfigRejectsUnsupportedValues(t *testing.T) {
	for _, field := range []struct {
		name string
		set  func(*config, string)
	}{
		{"MYSQL_HOST", func(c *config, v string) { c.host = v }},
		{"MYSQL_DATABASE", func(c *config, v string) { c.database = v }},
		{"MYSQL_USER", func(c *config, v string) { c.user = v }},
		{"MYSQL_PASSWORD", func(c *config, v string) { c.password = v }},
	} {
		for i, value := range []string{
			"secret\nmysql-debug=on", "secret\rvalue", "secret\x00value",
			`both'" quotes`, `both'",comma`, `'"leading`, `#both'"`,
			strings.Repeat("secret", 50),
		} {
			t.Run(field.name+"/"+strconv.Itoa(i), func(t *testing.T) {
				c := testConfig(t)
				field.set(&c, value)
				data, err := connectionConfig(c)
				if err == nil || !strings.Contains(err.Error(), field.name) || len(data) != 0 {
					t.Fatal("unsupported connection setting was not rejected safely")
				}
				if strings.Contains(err.Error(), value) || strings.Contains(err.Error(), "dummy-secret") {
					t.Fatal("configuration error exposed credentials")
				}
			})
		}
	}
}

func TestConnectionConfigLineLimit(t *testing.T) {
	c := testConfig(t)
	maximum := 254 - len("mysql-password=''\n")
	c.password = strings.Repeat("x", maximum)
	data, err := connectionConfig(c)
	if err != nil {
		t.Fatal("maximum supported password length was rejected")
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if len(line)+1 > 254 {
			t.Fatal("configuration exceeded the supported line size")
		}
	}
	c.password += "x"
	if _, err := connectionConfig(c); err == nil {
		t.Fatal("overlong configuration line was accepted")
	}
}

func TestWriteConnectionConfigFailuresLeaveNoFiles(t *testing.T) {
	temporaryRoot := t.TempDir()
	t.Setenv("TMPDIR", temporaryRoot)
	c := testConfig(t)
	c.password = "secret\nvalue"
	path, cleanup, err := writeConnectionConfig(c)
	if err == nil || path != "" || cleanup != nil {
		t.Fatal("invalid configuration produced a usable file")
	}
	entries, err := os.ReadDir(temporaryRoot)
	if err != nil || len(entries) != 0 {
		t.Fatal("invalid configuration created temporary files")
	}
	t.Setenv("TMPDIR", filepath.Join(temporaryRoot, "missing"))
	_, _, err = writeConnectionConfig(testConfig(t))
	if err == nil || strings.Contains(err.Error(), temporaryRoot) {
		t.Fatal("temporary directory failure was not reported safely")
	}
}

func TestConnectionConfigRoundTripsThroughSysbench(t *testing.T) {
	executable, err := exec.LookPath("sysbench")
	if err != nil {
		t.Skip("sysbench is not installed; run this check with the image's toolchain")
	}
	// Read sysbench's parsed C options without connecting to a database. Hex
	// output lets the assertion check exact bytes, including whitespace.
	script := filepath.Join(t.TempDir(), "inspect.lua")
	const inspect = `local ffi = require("ffi")
ffi.cdef[[const char *sb_get_value_string(const char *name);]]
function prepare()
  for _, name in ipairs({"mysql-host", "mysql-port", "mysql-db", "mysql-user", "mysql-password"}) do
    local value = ffi.string(ffi.C.sb_get_value_string(name))
    local bytes = {}
    for i = 1, #value do bytes[i] = string.format("%02x", string.byte(value, i)) end
    print(name .. ":" .. table.concat(bytes))
  end
end
`
	if err := os.WriteFile(script, []byte(inspect), 0600); err != nil {
		t.Fatal(err)
	}
	for i, value := range []string{
		"simple", " spaces \t ", "with'single", `with"double`, `both'"quotes`,
		"#hash,comma=equals", `back\slash`, `trailing\`, "unicode-λ",
		strings.Repeat("x", 254-len("mysql-password=''\n")),
	} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			c := testConfig(t)
			c.database, c.user, c.password = value, value, value
			path, cleanup, err := writeConnectionConfig(c)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "--config-file="+path, script, "prepare")
			cmd.Env = commandEnvironment(os.Environ())
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatal("could not inspect sysbench's parsed configuration")
			}
			for name, expected := range map[string]string{
				"mysql-host": c.host, "mysql-port": strconv.Itoa(c.port),
				"mysql-db": value, "mysql-user": value, "mysql-password": value,
			} {
				if !strings.Contains(string(output), name+":"+hex.EncodeToString([]byte(expected))+"\n") {
					t.Fatal("sysbench changed a connection value while parsing configuration")
				}
			}
		})
	}
}
