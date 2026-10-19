package pgbench

import (
	"strings"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

func validConnectionDetails() driver.ConnectionDetails {
	return driver.ConnectionDetails{Host: "postgres.example.com", Port: "5432", Username: "bench-user", Password: "credential-sentinel", Database: "app"}
}

func TestResolveConnectionDatabasePrecedence(t *testing.T) {
	for _, test := range []struct {
		name, requested, database, uri, want string
	}{
		{"request wins", " custom ", "app", "postgresql://u:p@host/other", "custom"},
		{"credential database", "", " app ", "postgresql://u:p@host/other", "app"},
		{"URI database", "", "", "postgresql://u:p@host/uri-db?sslmode=require", "uri-db"},
		{"postgres scheme", "", "", "postgres://u:p@host/uri-db", "uri-db"},
		{"escaped URI database", "", "", "postgresql://u:p@host/my%20db", "my db"},
		{"blank hints fall through", " \t", " \t", "postgresql://u:p@host/uri-db", "uri-db"},
		{"explicit database needs no URI", "custom", "", "malformed%URI", "custom"},
	} {
		t.Run(test.name, func(t *testing.T) {
			details := validConnectionDetails()
			details.Database, details.URI = test.database, test.uri
			got, err := resolveConnection(test.requested, details)
			if err != nil || got.Database != test.want {
				t.Fatalf("resolved database = %q, %v; want %q", got.Database, err, test.want)
			}
			if got.Host != details.Host || got.Username != details.Username || got.Password != details.Password || got.Port != 5432 {
				t.Fatal("connection fields were mapped incorrectly")
			}
		})
	}
}

func TestResolveConnectionRejectsInvalidDetails(t *testing.T) {
	for _, test := range []struct {
		name      string
		change    func(*driver.ConnectionDetails)
		requested string
		kind      driver.ErrorKind
	}{
		{"missing host", func(c *driver.ConnectionDetails) { c.Host = "" }, "", driver.InvalidCredentials},
		{"blank host", func(c *driver.ConnectionDetails) { c.Host = " \t" }, "", driver.InvalidCredentials},
		{"missing port", func(c *driver.ConnectionDetails) { c.Port = "" }, "", driver.InvalidCredentials},
		{"invalid port", func(c *driver.ConnectionDetails) { c.Port = "credential-sentinel" }, "", driver.InvalidCredentials},
		{"zero port", func(c *driver.ConnectionDetails) { c.Port = "0" }, "", driver.InvalidCredentials},
		{"negative port", func(c *driver.ConnectionDetails) { c.Port = "-1" }, "", driver.InvalidCredentials},
		{"too large port", func(c *driver.ConnectionDetails) { c.Port = "65536" }, "", driver.InvalidCredentials},
		{"missing user", func(c *driver.ConnectionDetails) { c.Username = "" }, "", driver.InvalidCredentials},
		{"blank user", func(c *driver.ConnectionDetails) { c.Username = " \t" }, "", driver.InvalidCredentials},
		{"missing password", func(c *driver.ConnectionDetails) { c.Password = "" }, "", driver.InvalidCredentials},
		{"blank password", func(c *driver.ConnectionDetails) { c.Password = " \t" }, "", driver.InvalidCredentials},
		{"URI-only credentials", func(c *driver.ConnectionDetails) {
			*c = driver.ConnectionDetails{URI: "postgresql://u:credential-sentinel@host/app"}
		}, "", driver.UnsupportedConnection},
		{"URI instead of password", func(c *driver.ConnectionDetails) {
			c.Password = ""
			c.URI = "postgresql://u:credential-sentinel@host/app"
		}, "", driver.UnsupportedConnection},
		{"missing database hints", func(c *driver.ConnectionDetails) { c.Database = "" }, "", driver.InvalidOptions},
		{"URI without database", func(c *driver.ConnectionDetails) { c.Database = ""; c.URI = "postgresql://u:credential-sentinel@host" }, "", driver.InvalidOptions},
		{"nested URI path", func(c *driver.ConnectionDetails) {
			c.Database = ""
			c.URI = "postgresql://u:credential-sentinel@host/a/b"
		}, "", driver.InvalidCredentials},
		{"escaped nested URI path", func(c *driver.ConnectionDetails) {
			c.Database = ""
			c.URI = "postgresql://u:credential-sentinel@host/a%2Fb"
		}, "", driver.InvalidCredentials},
		{"malformed URI", func(c *driver.ConnectionDetails) {
			c.Database = ""
			c.URI = "postgresql://u:credential-sentinel@host/%GG"
		}, "", driver.InvalidCredentials},
		{"unsupported URI scheme", func(c *driver.ConnectionDetails) { c.Database = ""; c.URI = "https://host/app" }, "", driver.InvalidCredentials},
		{"database connection string", func(c *driver.ConnectionDetails) { c.Database = "dbname=credential-sentinel" }, "", driver.InvalidCredentials},
		{"database connection URI", func(c *driver.ConnectionDetails) { c.Database = "POSTGRESQL://u:credential-sentinel@host/app" }, "", driver.InvalidCredentials},
		{"URI database connection string", func(c *driver.ConnectionDetails) {
			c.Database = ""
			c.URI = "postgresql://u:p@host/dbname%3Dcredential-sentinel"
		}, "", driver.InvalidCredentials},
		{"URI database connection URI", func(c *driver.ConnectionDetails) {
			c.Database = ""
			c.URI = "postgresql://u:p@host/postgres%3A%2F%2Fu%3Acredential-sentinel%40h%2Fapp"
		}, "", driver.InvalidCredentials},
		{"requested connection string", func(*driver.ConnectionDetails) {}, "dbname=credential-sentinel", driver.InvalidOptions},
		{"requested postgres URI", func(*driver.ConnectionDetails) {}, " postgres://u:credential-sentinel@host/app ", driver.InvalidOptions},
		{"requested postgresql URI", func(*driver.ConnectionDetails) {}, "POSTGRESQL://u:credential-sentinel@host/app", driver.InvalidOptions},
		{"NUL in host", func(c *driver.ConnectionDetails) { c.Host += "\x00" }, "", driver.InvalidCredentials},
		{"NUL in user", func(c *driver.ConnectionDetails) { c.Username += "\x00" }, "", driver.InvalidCredentials},
		{"NUL in password", func(c *driver.ConnectionDetails) { c.Password += "\x00" }, "", driver.InvalidCredentials},
		{"NUL in database", func(c *driver.ConnectionDetails) { c.Database += "\x00" }, "", driver.InvalidCredentials},
		{"NUL in requested database", func(*driver.ConnectionDetails) {}, "app\x00", driver.InvalidOptions},
	} {
		t.Run(test.name, func(t *testing.T) {
			details := validConnectionDetails()
			test.change(&details)
			got, err := resolveConnection(test.requested, details)
			assertErrorKind(t, err, test.kind)
			if got != (resolvedConnection{}) {
				t.Fatal("invalid credentials produced a usable connection")
			}
			if strings.Contains(err.Error(), "credential-sentinel") {
				t.Fatal("connection error leaked credentials")
			}
		})
	}
}

func TestResolveConnectionNormalizesPortAndPreservesPassword(t *testing.T) {
	for _, test := range []struct {
		port string
		want int
	}{{"1", 1}, {"65535", 65535}, {" 05432 ", 5432}} {
		details := validConnectionDetails()
		details.Port, details.Password = test.port, "  credential-sentinel  "
		got, err := resolveConnection("", details)
		if err != nil || got.Password != details.Password || got.Port != test.want {
			t.Fatalf("port %q: connection rejected, password changed, or port not normalized: %v", test.port, err)
		}
	}
}
