package hammerdb

import (
	"errors"
	"strings"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

func validConnectionDetails() driver.ConnectionDetails {
	return driver.ConnectionDetails{
		Host: "db.example.com", Port: "5432", Username: "bench-user",
		Password: "credential-sentinel", Database: "app",
	}
}

func TestResolveConnectionDatabasePrecedence(t *testing.T) {
	for _, test := range []struct {
		name, databaseType, requested, credentialDatabase, uri, want string
	}{
		{"requested database wins", "postgresql", " requested ", "upstream", "postgresql://u:uri-credential-sentinel%21@host/from-uri", "requested"},
		{"credential database wins", "postgresql", "", " upstream ", "postgresql://u:uri-credential-sentinel%21@host/from-uri", "upstream"},
		{"PostgreSQL URI database", "postgresql", "", "", "postgresql://u:uri-credential-sentinel%21@host/from-uri", "from-uri"},
		{"postgres URI scheme", "postgresql", "", "", "postgres://u:uri-credential-sentinel%21@host/from-uri", "from-uri"},
		{"MySQL URI database", "mysql", "", "", "mysql://u:uri-credential-sentinel%21@host/from-uri", "from-uri"},
		{"URI query does not replace path", "mysql", "", "", "mysql://u:uri-credential-sentinel%21@host/from-uri?database=other", "from-uri"},
		{"blank hints fall through", "mysql", " \t", " \t", "mysql://u:uri-credential-sentinel%21@host/from-uri", "from-uri"},
	} {
		t.Run(test.name, func(t *testing.T) {
			details := validConnectionDetails()
			details.Database, details.URI = test.credentialDatabase, test.uri
			got, err := resolveConnection(driver.TargetInfo{DatabaseType: test.databaseType}, test.requested, details)
			if err != nil || got.Database != test.want {
				t.Fatalf("resolved database = %q, %v; want %q", got.Database, err, test.want)
			}
			if got.DatabaseType != test.databaseType || got.Host != details.Host || got.Username != details.Username || got.Password != details.Password || got.URI != details.URI {
				t.Fatal("connection details were not preserved")
			}
			if got.Port != 5432 {
				t.Fatalf("port = %d; want 5432", got.Port)
			}
		})
	}
}

func TestResolveConnectionRequiresExplicitDatabaseWhenNoHintExists(t *testing.T) {
	for _, test := range []struct{ databaseType, uri string }{
		{"mysql", ""},
		{"mysql", "mysql://u:uri-credential-sentinel%21@host:3306?tls=false"},
		{"mysql", "mysql://u:uri-credential-sentinel%21@host:3306/?tls=false"},
		{"postgresql", "postgresql://u:uri-credential-sentinel%21@host:5432/"},
	} {
		t.Run(test.databaseType+"/"+test.uri, func(t *testing.T) {
			details := validConnectionDetails()
			details.Database, details.URI = "", test.uri
			target := driver.TargetInfo{DatabaseType: test.databaseType}
			got, err := resolveConnection(target, "", details)
			assertConnectionError(t, err, driver.InvalidOptions)
			if got != (resolvedConnection{}) || !strings.Contains(err.Error(), "enter a database name") {
				t.Fatal("missing database must return an empty connection and ask for a name")
			}
			assertNoConnectionSecrets(t, err, details)
			got, err = resolveConnection(target, "explicit_db", details)
			if err != nil || got.Database != "explicit_db" {
				t.Fatalf("explicit database rejected: %v", err)
			}
		})
	}
}

func TestResolveConnectionTransportDefaultsAndURIOverrides(t *testing.T) {
	for _, test := range []struct {
		name         string
		databaseType string
		uri          string
		pgSSLMode    string
		mysqlSSL     bool
	}{
		{"PostgreSQL default", "postgresql", "", "prefer", false},
		{"PostgreSQL disable", "postgresql", "postgresql://u:uri-credential-sentinel%21@host/app?sslmode=disable", "disable", false},
		{"PostgreSQL require", "postgresql", "postgres://u:uri-credential-sentinel%21@host/app?sslmode=require", "require", false},
		{"MySQL default", "mysql", "", "", true},
		{"MySQL explicit true", "mysql", "mysql://u:uri-credential-sentinel%21@host/app?tls=true", "", true},
		{"MySQL explicit false", "mysql", "mysql://u:uri-credential-sentinel%21@host/app?tls=false", "", false},
		{"MySQL URI without database", "mysql", "mysql://u:uri-credential-sentinel%21@host:3306/?tls=false", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			details := validConnectionDetails()
			details.URI = test.uri
			got, err := resolveConnection(driver.TargetInfo{DatabaseType: test.databaseType}, "app", details)
			if err != nil {
				t.Fatalf("resolveConnection() error = %v", err)
			}
			if got.PGSSLMode != test.pgSSLMode || got.MySQLSSL != test.mysqlSSL {
				t.Fatalf("TLS settings = (%q, %t); want (%q, %t)", got.PGSSLMode, got.MySQLSSL, test.pgSSLMode, test.mysqlSSL)
			}
		})
	}
}

func TestResolveConnectionRejectsUnsupportedOrMalformedTLSHints(t *testing.T) {
	for _, test := range []struct {
		name, databaseType, uri string
		kind                    driver.ErrorKind
	}{
		{"PostgreSQL verify CA", "postgresql", "postgresql://u:uri-credential-sentinel%21@host/app?sslmode=verify-ca", driver.UnsupportedConnection},
		{"PostgreSQL verify identity", "postgresql", "postgresql://u:uri-credential-sentinel%21@host/app?sslmode=verify-full", driver.UnsupportedConnection},
		{"PostgreSQL allow mode", "postgresql", "postgresql://u:uri-credential-sentinel%21@host/app?sslmode=allow", driver.UnsupportedConnection},
		{"PostgreSQL CA option", "postgresql", "postgresql://u:uri-credential-sentinel%21@host/app?sslrootcert=%2Fca.pem", driver.UnsupportedConnection},
		{"PostgreSQL rejects MySQL CA option", "postgresql", "postgresql://u:uri-credential-sentinel%21@host/app?tlsCA=%2Fca.pem", driver.UnsupportedConnection},
		{"PostgreSQL repeated sslmode", "postgresql", "postgresql://u:uri-credential-sentinel%21@host/app?sslmode=require&sslmode=disable", driver.InvalidCredentials},
		{"PostgreSQL malformed query", "postgresql", "postgresql://u:uri-credential-sentinel%21@host/app?sslmode=require;tls=false", driver.InvalidCredentials},
		{"PostgreSQL invalid sslmode", "postgresql", "postgresql://u:uri-credential-sentinel%21@host/app?sslmode=invalid", driver.InvalidCredentials},
		{"MySQL CA option", "mysql", "mysql://u:uri-credential-sentinel%21@host/app?tlsCA=%2Fca.pem", driver.UnsupportedConnection},
		{"MySQL rejects PostgreSQL CA option", "mysql", "mysql://u:uri-credential-sentinel%21@host/app?sslrootcert=%2Fca.pem", driver.UnsupportedConnection},
		{"MySQL client certificate", "mysql", "mysql://u:uri-credential-sentinel%21@host/app?tlsCertificateKeyFile=%2Fclient.pem", driver.UnsupportedConnection},
		{"MySQL standard identity verification", "mysql", "mysql://u:uri-credential-sentinel%21@host/app?ssl-mode=VERIFY_IDENTITY&ssl-ca=%2Fca.pem", driver.UnsupportedConnection},
		{"MySQL conflicting required TLS", "mysql", "mysql://u:uri-credential-sentinel%21@host/app?tls=false&ssl-mode=REQUIRED", driver.UnsupportedConnection},
		{"MySQL standard CA option", "mysql", "mysql://u:uri-credential-sentinel%21@host/app?ssl-ca=%2Fca.pem", driver.UnsupportedConnection},
		{"MySQL standard client key", "mysql", "mysql://u:uri-credential-sentinel%21@host/app?ssl-key=%2Fclient.key", driver.UnsupportedConnection},
		{"PostgreSQL rejects MySQL verification mode", "postgresql", "postgresql://u:uri-credential-sentinel%21@host/app?ssl-mode=VERIFY_IDENTITY", driver.UnsupportedConnection},
		{"MySQL repeated TLS setting", "mysql", "mysql://u:uri-credential-sentinel%21@host/app?tls=true&TLS=false", driver.InvalidCredentials},
		{"MySQL repeated TLS values", "mysql", "mysql://u:uri-credential-sentinel%21@host/app?tls=true&tls=false", driver.InvalidCredentials},
		{"MySQL invalid TLS value", "mysql", "mysql://u:uri-credential-sentinel%21@host/app?tls=maybe", driver.InvalidCredentials},
		{"MySQL PostgreSQL TLS option", "mysql", "mysql://u:uri-credential-sentinel%21@host/app?sslmode=require", driver.InvalidCredentials},
		{"PostgreSQL MySQL TLS option", "postgresql", "postgresql://u:uri-credential-sentinel%21@host/app?tls=false", driver.InvalidCredentials},
	} {
		t.Run(test.name, func(t *testing.T) {
			details := validConnectionDetails()
			details.URI = test.uri
			got, err := resolveConnection(driver.TargetInfo{DatabaseType: test.databaseType}, "app", details)
			assertConnectionError(t, err, test.kind)
			if got != (resolvedConnection{}) {
				t.Fatal("unsupported TLS configuration returned a usable connection")
			}
			assertNoConnectionSecrets(t, err, details)
		})
	}
}

func TestResolveConnectionRejectsUnsupportedOrInvalidDetails(t *testing.T) {
	for _, test := range []struct {
		name      string
		target    string
		requested string
		change    func(*driver.ConnectionDetails)
		kind      driver.ErrorKind
	}{
		{"unsupported database type", "mongodb", "", func(*driver.ConnectionDetails) {}, driver.UnsupportedConnection},
		{"missing database type", "", "", func(*driver.ConnectionDetails) {}, driver.UnsupportedConnection},
		{"missing host", "mysql", "", func(c *driver.ConnectionDetails) { c.Host = "" }, driver.InvalidCredentials},
		{"host with port", "mysql", "", func(c *driver.ConnectionDetails) { c.Host = "db.example.com:3306" }, driver.InvalidCredentials},
		{"multiple hosts", "mysql", "", func(c *driver.ConnectionDetails) { c.Host = "db-a,db-b" }, driver.InvalidCredentials},
		{"invalid DNS label", "mysql", "", func(c *driver.ConnectionDetails) { c.Host = "-bad.example" }, driver.InvalidCredentials},
		{"NUL in host", "mysql", "", func(c *driver.ConnectionDetails) { c.Host += "\x00" }, driver.InvalidCredentials},
		{"missing port", "mysql", "", func(c *driver.ConnectionDetails) { c.Port = "" }, driver.InvalidCredentials},
		{"invalid port", "mysql", "", func(c *driver.ConnectionDetails) { c.Port = "credential-sentinel" }, driver.InvalidCredentials},
		{"zero port", "mysql", "", func(c *driver.ConnectionDetails) { c.Port = "0" }, driver.InvalidCredentials},
		{"port too large", "mysql", "", func(c *driver.ConnectionDetails) { c.Port = "65536" }, driver.InvalidCredentials},
		{"missing username", "mysql", "", func(c *driver.ConnectionDetails) { c.Username = "" }, driver.InvalidCredentials},
		{"unsupported username characters", "mysql", "", func(c *driver.ConnectionDetails) { c.Username = "user name" }, driver.InvalidCredentials},
		{"MySQL username too long", "mysql", "", func(c *driver.ConnectionDetails) { c.Username = strings.Repeat("u", 33) }, driver.InvalidCredentials},
		{"missing password", "mysql", "", func(c *driver.ConnectionDetails) { c.Password = "" }, driver.InvalidCredentials},
		{"password edge whitespace", "mysql", "", func(c *driver.ConnectionDetails) { c.Password = " secret " }, driver.InvalidCredentials},
		{"password double quote", "mysql", "", func(c *driver.ConnectionDetails) { c.Password = `secret"value` }, driver.InvalidCredentials},
		{"password NUL", "mysql", "", func(c *driver.ConnectionDetails) { c.Password += "\x00" }, driver.InvalidCredentials},
		{"URI-only credentials", "mysql", "", func(c *driver.ConnectionDetails) {
			*c = driver.ConnectionDetails{URI: "mysql://u:uri-credential-sentinel%21@host/app"}
		}, driver.UnsupportedConnection},
		{"URI missing database path", "mysql", "", func(c *driver.ConnectionDetails) {
			c.Database = ""
			c.URI = "mysql://u:uri-credential-sentinel%21@host"
		}, driver.InvalidOptions},
		{"URI nested path", "mysql", "", func(c *driver.ConnectionDetails) {
			c.Database = ""
			c.URI = "mysql://u:uri-credential-sentinel%21@host/a/b"
		}, driver.InvalidCredentials},
		{"URI escaped slash", "postgresql", "", func(c *driver.ConnectionDetails) {
			c.Database = ""
			c.URI = "postgresql://u:uri-credential-sentinel%21@host/a%2Fb"
		}, driver.InvalidCredentials},
		{"URI invalid escape", "postgresql", "", func(c *driver.ConnectionDetails) {
			c.Database = ""
			c.URI = "postgresql://u:uri-credential-sentinel%21@host/%GG"
		}, driver.InvalidCredentials},
		{"URI scheme does not match engine", "mysql", "", func(c *driver.ConnectionDetails) {
			c.Database = ""
			c.URI = "postgresql://u:uri-credential-sentinel%21@host/app"
		}, driver.InvalidCredentials},
		{"URI control character", "mysql", "", func(c *driver.ConnectionDetails) {
			c.Database = ""
			c.URI = "mysql://u:uri-credential-sentinel%21@host/app\n"
		}, driver.InvalidCredentials},
		{"malformed URI despite explicit database", "mysql", "requested", func(c *driver.ConnectionDetails) { c.URI = "malformed%URI" }, driver.InvalidCredentials},
		{"requested database invalid", "mysql", "bad/name", func(*driver.ConnectionDetails) {}, driver.InvalidOptions},
		{"requested database too long", "mysql", strings.Repeat("d", 65), func(*driver.ConnectionDetails) {}, driver.InvalidOptions},
		{"upstream database invalid", "mysql", "", func(c *driver.ConnectionDetails) { c.Database = "bad/name" }, driver.InvalidCredentials},
		{"URI database invalid", "mysql", "", func(c *driver.ConnectionDetails) {
			c.Database = ""
			c.URI = "mysql://u:uri-credential-sentinel%21@host/bad%2Fname"
		}, driver.InvalidCredentials},
	} {
		t.Run(test.name, func(t *testing.T) {
			details := validConnectionDetails()
			test.change(&details)
			got, err := resolveConnection(driver.TargetInfo{DatabaseType: test.target}, test.requested, details)
			assertConnectionError(t, err, test.kind)
			if got != (resolvedConnection{}) {
				t.Fatal("invalid connection returned usable details")
			}
			assertNoConnectionSecrets(t, err, details)
		})
	}
}

func TestResolveConnectionAcceptsMaximumEngineDatabaseNames(t *testing.T) {
	for _, test := range []struct {
		databaseType string
		length       int
	}{{"postgresql", 63}, {"mysql", 64}} {
		details := validConnectionDetails()
		details.Database = ""
		got, err := resolveConnection(driver.TargetInfo{DatabaseType: test.databaseType}, strings.Repeat("d", test.length), details)
		if err != nil || len(got.Database) != test.length {
			t.Fatalf("%s database length %d rejected: %v", test.databaseType, test.length, err)
		}
	}
}

func assertConnectionError(t *testing.T, err error, kind driver.ErrorKind) {
	t.Helper()
	var problem *driver.Error
	if !errors.As(err, &problem) || problem.Kind != kind {
		t.Fatalf("error = %v; want driver error kind %q", err, kind)
	}
}

func assertNoConnectionSecrets(t *testing.T, err error, details driver.ConnectionDetails) {
	t.Helper()
	for _, secret := range []string{details.Password, details.URI, "uri-credential-sentinel!", "uri-credential-sentinel%21"} {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatal("connection error exposed credentials or their encoded representation")
		}
	}
}
