package main

import (
	"strconv"
	"strings"
	"testing"
)

func validEnvironment() map[string]string {
	return map[string]string{
		"MYSQL_HOST":     "mysql",
		"MYSQL_DATABASE": "sbtest",
		"MYSQL_USER":     "sbuser",
		"MYSQL_PASSWORD": "dummy-secret",
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	env := validEnvironment()
	got, err := loadConfig(func(key string) string { return env[key] })
	want := config{
		host: "mysql", database: "sbtest", user: "sbuser", password: "dummy-secret",
		port: 3306, duration: 60, threads: 1, tables: 1, tableSize: 10000,
	}
	if err != nil || got != want {
		t.Fatal("configuration did not preserve connection settings and apply the expected defaults")
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	env := validEnvironment()
	for key, value := range map[string]string{
		"MYSQL_HOST": "127.0.0.1", "MYSQL_DATABASE": "custom_bench", "MYSQL_USER": "custom_user",
		"MYSQL_PASSWORD": "  p@ss,'\"#\\word  ", "MYSQL_PORT": "3307",
		"BENCH_DURATION": "30", "BENCH_THREADS": "4", "BENCH_TABLES": "2",
		"BENCH_TABLE_SIZE": "5000", "BENCH_INITIALIZE": "true",
	} {
		env[key] = value
	}
	got, err := loadConfig(func(key string) string { return env[key] })
	want := config{
		host: "127.0.0.1", database: "custom_bench", user: "custom_user", password: env["MYSQL_PASSWORD"],
		port: 3307, duration: 30, threads: 4, tables: 2, tableSize: 5000, initialize: true,
	}
	if err != nil || got != want {
		t.Fatal("configuration did not preserve explicit connection and benchmark settings")
	}
}

func TestLoadConfigRejectsInvalidConnection(t *testing.T) {
	for _, field := range []string{"MYSQL_HOST", "MYSQL_DATABASE", "MYSQL_USER", "MYSQL_PASSWORD"} {
		values := []string{"", "invalid\x00value"}
		if field != "MYSQL_PASSWORD" {
			values = append(values, " \t\n")
		}
		for i, value := range values {
			t.Run(field+"/"+strconv.Itoa(i), func(t *testing.T) {
				env := validEnvironment()
				env[field] = value
				assertConfigError(t, env, field)
			})
		}
	}
	t.Run("multiple hosts", func(t *testing.T) {
		env := validEnvironment()
		env["MYSQL_HOST"] = "mysql-a,mysql-b"
		assertConfigError(t, env, "MYSQL_HOST")
	})
}

func TestLoadConfigPreservesConnectionValues(t *testing.T) {
	for _, host := range []string{"mysql.default.svc", "127.0.0.1", "::1"} {
		env := validEnvironment()
		env["MYSQL_HOST"] = host
		env["MYSQL_DATABASE"] = " bench database "
		env["MYSQL_USER"] = " bench user "
		env["MYSQL_PASSWORD"] = " \t "
		got, err := loadConfig(func(key string) string { return env[key] })
		if err != nil || got.host != host || got.database != env["MYSQL_DATABASE"] ||
			got.user != env["MYSQL_USER"] || got.password != env["MYSQL_PASSWORD"] {
			t.Fatal("connection configuration was rejected or silently changed")
		}
	}
}

func TestLoadConfigNumericBounds(t *testing.T) {
	for _, field := range []struct {
		name    string
		maximum int
		read    func(config) int
	}{
		{"MYSQL_PORT", 65535, func(c config) int { return c.port }},
		{"BENCH_DURATION", 2147483647, func(c config) int { return c.duration }},
		{"BENCH_THREADS", 2147483647, func(c config) int { return c.threads }},
		{"BENCH_TABLES", 2147483647, func(c config) int { return c.tables }},
		{"BENCH_TABLE_SIZE", 2147483647, func(c config) int { return c.tableSize }},
	} {
		t.Run(field.name, func(t *testing.T) {
			for _, value := range []string{
				"0", "00", "-1", "+1", "1.5", "1e3", "abc", " ", " 1", "1 ",
				"2147483648", "999999999999999999999999", strconv.Itoa(field.maximum + 1),
			} {
				env := validEnvironment()
				env[field.name] = value
				// Table settings remain meaningful when existing tables are reused.
				env["BENCH_INITIALIZE"] = "false"
				assertConfigError(t, env, field.name)
			}
			for _, value := range []int{1, field.maximum} {
				env := validEnvironment()
				env[field.name] = strconv.Itoa(value)
				got, err := loadConfig(func(key string) string { return env[key] })
				if err != nil || field.read(got) != value {
					t.Fatalf("valid boundary for %s was rejected or changed", field.name)
				}
			}
		})
	}
}

func TestLoadConfigInitialization(t *testing.T) {
	for _, value := range []string{"", "false", "true"} {
		env := validEnvironment()
		env["BENCH_INITIALIZE"] = value
		got, err := loadConfig(func(key string) string { return env[key] })
		if err != nil || got.initialize != (value == "true") {
			t.Fatalf("unexpected initialization setting for %q", value)
		}
	}
	for _, value := range []string{"TRUE", "False", "1", "0", "yes", " true ", "dummy-secret"} {
		env := validEnvironment()
		env["BENCH_INITIALIZE"] = value
		assertConfigError(t, env, "BENCH_INITIALIZE")
	}
}

func assertConfigError(t *testing.T, env map[string]string, field string) {
	t.Helper()
	got, err := loadConfig(func(key string) string { return env[key] })
	if err == nil {
		t.Fatalf("expected a configuration error for %s", field)
	}
	if !strings.Contains(err.Error(), field) {
		t.Fatalf("error did not identify the invalid setting %s", field)
	}
	// Short numeric inputs can also occur in the documented valid range.
	if strings.Contains(err.Error(), "dummy-secret") ||
		(len(env[field]) >= 8 && strings.Contains(err.Error(), env[field])) {
		t.Fatal("configuration error exposed a supplied value")
	}
	if got != (config{}) {
		t.Fatal("configuration error returned partially populated credentials")
	}
}
