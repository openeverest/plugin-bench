package main

import (
	"strings"
	"testing"
)

func validEnvironment(engine string) map[string]string {
	return map[string]string{
		"DB_TYPE": engine, "DB_HOST": "database.default.svc",
		"DB_NAME": "benchmark", "DB_USER": "inst-h7w", "DB_PASSWORD": "dummy-secret",
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	for _, engine := range []string{"postgresql", "mysql"} {
		t.Run(engine, func(t *testing.T) {
			env := validEnvironment(engine)
			got, err := loadConfig(func(key string) string { return env[key] })
			want := config{
				databaseType: engine, workload: "tproc-c", host: env["DB_HOST"],
				database: "benchmark", user: "inst-h7w", password: "dummy-secret",
				warehouses: 1, virtualUsers: 1, durationMinutes: 1,
			}
			if engine == "postgresql" {
				want.port, want.pgSSLMode = 5432, "prefer"
			} else {
				want.port, want.mysqlSSL = 3306, true
			}
			if err != nil || got != want {
				t.Fatal("connection settings or engine defaults were not preserved")
			}
		})
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	for _, engine := range []string{"postgresql", "mysql"} {
		t.Run(engine, func(t *testing.T) {
			env := validEnvironment(engine)
			for key, value := range map[string]string{
				"DB_HOST": "::1", "DB_PORT": "5433", "DB_NAME": "bench-data.v1",
				"DB_USER": "bench_user", "DB_PASSWORD": "pa$$[word];{x}\\'",
				"BENCH_WORKLOAD": "tproc-c", "BENCH_WAREHOUSES": "4", "BENCH_VIRTUAL_USERS": "2",
				"BENCH_RAMPUP_MINUTES": "2", "BENCH_DURATION_MINUTES": "3",
				"BENCH_INITIALIZE": "true", "PGSSLMODE": "require", "MYSQL_SSL": "false",
			} {
				env[key] = value
			}
			got, err := loadConfig(func(key string) string { return env[key] })
			if err != nil {
				t.Fatalf("valid overrides rejected: %v", err)
			}
			if got.host != "::1" || got.port != 5433 || got.database != "bench-data.v1" ||
				got.user != "bench_user" || got.password != env["DB_PASSWORD"] ||
				got.warehouses != 4 || got.virtualUsers != 2 || got.rampupMinutes != 2 ||
				got.durationMinutes != 3 || !got.initialize {
				t.Fatal("connection or workload overrides were changed")
			}
			if engine == "postgresql" && got.pgSSLMode != "require" || engine == "mysql" && got.mysqlSSL {
				t.Fatal("engine-specific TLS override was ignored")
			}
		})
	}
}

func assertConfigError(t *testing.T, env map[string]string, field string) {
	t.Helper()
	got, err := loadConfig(func(key string) string { return env[key] })
	if err == nil || !strings.Contains(err.Error(), field) {
		t.Fatalf("expected an error identifying %s", field)
	}
	if got != (config{}) {
		t.Fatal("invalid configuration returned partially populated credentials")
	}
	if strings.Contains(err.Error(), "dummy-secret") || strings.Contains(err.Error(), "sentinel-private") {
		t.Fatal("configuration error exposed a supplied value")
	}
}

func TestLoadConfigRejectsInvalidInput(t *testing.T) {
	tests := []struct{ name, field, value string }{
		{"missing type", "DB_TYPE", ""},
		{"unsupported type", "DB_TYPE", "mongodb"},
		{"unsupported workload", "BENCH_WORKLOAD", "tproc-h"},
		{"port zero", "DB_PORT", "0"},
		{"port too large", "DB_PORT", "65536"},
		{"zero warehouses", "BENCH_WAREHOUSES", "0"},
		{"zero users", "BENCH_VIRTUAL_USERS", "0"},
		{"negative rampup", "BENCH_RAMPUP_MINUTES", "-1"},
		{"zero duration", "BENCH_DURATION_MINUTES", "0"},
		{"fractional duration", "BENCH_DURATION_MINUTES", "1.5"},
		{"numeric whitespace", "BENCH_WAREHOUSES", " 1"},
		{"numeric overflow", "BENCH_WAREHOUSES", "2147483648"},
		{"huge numeric input", "BENCH_VIRTUAL_USERS", "999999999999999999999999"},
		{"invalid boolean", "BENCH_INITIALIZE", "TRUE"},
		{"secret boolean", "BENCH_INITIALIZE", "sentinel-private"},
		{"unsupported TLS mode", "PGSSLMODE", "verify-full"},
		{"NUL password", "DB_PASSWORD", "sentinel-private\x00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnvironment("postgresql")
			env[tc.field] = tc.value
			assertConfigError(t, env, tc.field)
		})
	}
	for _, field := range []string{"DB_HOST", "DB_NAME", "DB_USER", "DB_PASSWORD"} {
		t.Run("missing/"+field, func(t *testing.T) {
			env := validEnvironment("postgresql")
			delete(env, field)
			assertConfigError(t, env, field)
		})
	}
}

func TestLoadConfigConnectionConstraints(t *testing.T) {
	for _, field := range []string{"DB_NAME", "DB_USER"} {
		for _, value := range []string{" ", "a'b", "a\"b", "a`b", "[puts sentinel-private]", "$name", "a;b", "a\\b", "a\nb", "a\x00b"} {
			env := validEnvironment("postgresql")
			env[field] = value
			assertConfigError(t, env, field)
		}
	}
	for _, host := range []string{"localhost", "db.default.svc", "db.example.org.", "127.0.0.1", "2001:db8::1"} {
		env := validEnvironment("postgresql")
		env["DB_HOST"] = host
		got, err := loadConfig(func(key string) string { return env[key] })
		if err != nil || got.host != host {
			t.Fatal("valid hostname/IP was rejected or changed")
		}
	}
	for _, host := range []string{"db-a,db-b", "db:5432", "postgres://db", "/tmp/socket", "db host", "db\n", "[puts sentinel-private]", "db..svc"} {
		env := validEnvironment("postgresql")
		env["DB_HOST"] = host
		assertConfigError(t, env, "DB_HOST")
	}
	for _, engine := range []string{"postgresql", "mysql"} {
		env := validEnvironment(engine)
		nameLimit, userLimit := 63, 63
		if engine == "mysql" {
			nameLimit, userLimit = 64, 32
		}
		for field, maximum := range map[string]int{"DB_NAME": nameLimit, "DB_USER": userLimit} {
			env[field] = strings.Repeat("a", maximum)
			if _, err := loadConfig(func(key string) string { return env[key] }); err != nil {
				t.Fatalf("valid %s name boundary rejected: %v", engine, err)
			}
			env[field] += "a"
			assertConfigError(t, env, field)
			env[field] = "benchmark"
		}
	}
}

func TestLoadConfigPasswordConstraints(t *testing.T) {
	for _, engine := range []string{"postgresql", "mysql"} {
		for _, password := range []string{
			" sentinel-private", "sentinel-private ", "\tsentinel-private", "sentinel-private\t",
			"\nsentinel-private", "sentinel-private\r\n", " \t\n ",
			`sentinel-"private`, string([]byte{0xff}),
		} {
			env := validEnvironment(engine)
			env["DB_PASSWORD"] = password
			assertConfigError(t, env, "DB_PASSWORD")
		}
	}
}

func TestLoadConfigTimingAndConcurrency(t *testing.T) {
	env := validEnvironment("postgresql")
	env["BENCH_VIRTUAL_USERS"] = "2"
	assertConfigError(t, env, "BENCH_VIRTUAL_USERS")
	env["BENCH_WAREHOUSES"] = "2"
	env["BENCH_RAMPUP_MINUTES"] = "0"
	env["BENCH_DURATION_MINUTES"] = "153722867"
	if _, err := loadConfig(func(key string) string { return env[key] }); err != nil {
		t.Fatalf("representable time boundary rejected: %v", err)
	}
	env["BENCH_RAMPUP_MINUTES"] = "1"
	assertConfigError(t, env, "BENCH_RAMPUP_MINUTES")
	env["BENCH_RAMPUP_MINUTES"], env["BENCH_DURATION_MINUTES"] = "2147483647", "2147483647"
	assertConfigError(t, env, "BENCH_DURATION_MINUTES")
}

func TestLoadConfigTLSAndInitialization(t *testing.T) {
	for _, value := range []string{"false", "true"} {
		env := validEnvironment("mysql")
		env["MYSQL_SSL"], env["BENCH_INITIALIZE"] = value, value
		got, err := loadConfig(func(key string) string { return env[key] })
		if err != nil || got.mysqlSSL != (value == "true") || got.initialize != (value == "true") {
			t.Fatal("explicit boolean was rejected or changed")
		}
	}
	env := validEnvironment("mysql")
	env["MYSQL_SSL"] = "yes"
	assertConfigError(t, env, "MYSQL_SSL")
	for _, mode := range []string{"disable", "prefer", "require"} {
		env := validEnvironment("postgresql")
		env["PGSSLMODE"] = mode
		env["MYSQL_SSL"] = "irrelevant-to-postgresql"
		got, err := loadConfig(func(key string) string { return env[key] })
		if err != nil || got.pgSSLMode != mode {
			t.Fatal("valid PostgreSQL TLS mode rejected or changed")
		}
	}
	env = validEnvironment("mysql")
	env["PGSSLMODE"] = "irrelevant-to-mysql"
	if _, err := loadConfig(func(key string) string { return env[key] }); err != nil {
		t.Fatal("MySQL configuration interpreted a PostgreSQL-only setting")
	}
}
