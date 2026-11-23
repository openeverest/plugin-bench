package sysbench

import (
	"strconv"
	"strings"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

func validConnectionDetails() driver.ConnectionDetails {
	return driver.ConnectionDetails{Host: "mysql.example.com", Port: "3306", Username: "sbuser", Password: "credential-sentinel", Database: "sbtest"}
}

func TestResolveConnectionDatabasePrecedence(t *testing.T) {
	for _, test := range []struct {
		name, requested, database, uri, want string
	}{
		{"request wins", " custom ", "upstream", "mysql://other:secret@other-host/uri-db", "custom"},
		{"credential database", "", " upstream ", "mysql://u:p@host/uri-db", "upstream"},
		{"URI database", "", "", "mysql://u:p@host/uri-db", "uri-db"},
		{"case insensitive scheme", "", "", "MYSQL://u:p@host/uri-db", "uri-db"},
		{"escaped URI database", "", "", "mysql://u:p@host/my%20db", "my db"},
		{"blank hints fall through", " \t", " \t", "mysql://u:p@host/uri-db", "uri-db"},
		{"request ignores bad hints", "custom", "bad\nvalue", "malformed%URI", "custom"},
		{"credential database ignores bad URI", "", "upstream", "malformed%URI", "upstream"},
		{"URI query cannot override fields", "", "", "mysql://other:secret@other-host:9999/uri-db?database=other&host=other&password=other", "uri-db"},
		{"equals is a database name", "dbname=app", "", "", "dbname=app"},
	} {
		t.Run(test.name, func(t *testing.T) {
			details := validConnectionDetails()
			details.Database, details.URI = test.database, test.uri
			got, err := resolveConnection(test.requested, details)
			if err != nil || got.Database != test.want {
				t.Fatalf("database = %q, error = %v; want %q", got.Database, err, test.want)
			}
			if got.Host != details.Host || got.Username != details.Username || got.Password != details.Password || got.Port != 3306 || got.URI != details.URI {
				t.Fatal("structured credentials were changed or the original URI was lost")
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
		{"multiple hosts", func(c *driver.ConnectionDetails) { c.Host = "host-a,host-b" }, "", driver.InvalidCredentials},
		{"missing user", func(c *driver.ConnectionDetails) { c.Username = "" }, "", driver.InvalidCredentials},
		{"blank user", func(c *driver.ConnectionDetails) { c.Username = " \t" }, "", driver.InvalidCredentials},
		{"missing password", func(c *driver.ConnectionDetails) { c.Password = "" }, "", driver.InvalidCredentials},
		{"missing port", func(c *driver.ConnectionDetails) { c.Port = "" }, "", driver.InvalidCredentials},
		{"bad port", func(c *driver.ConnectionDetails) { c.Port = "credential-sentinel" }, "", driver.InvalidCredentials},
		{"zero port", func(c *driver.ConnectionDetails) { c.Port = "0" }, "", driver.InvalidCredentials},
		{"negative port", func(c *driver.ConnectionDetails) { c.Port = "-1" }, "", driver.InvalidCredentials},
		{"large port", func(c *driver.ConnectionDetails) { c.Port = "65536" }, "", driver.InvalidCredentials},
		{"overflow port", func(c *driver.ConnectionDetails) { c.Port = "99999999999999999999999999" }, "", driver.InvalidCredentials},
		{"fractional port", func(c *driver.ConnectionDetails) { c.Port = "3306.5" }, "", driver.InvalidCredentials},
		{"port newline", func(c *driver.ConnectionDetails) { c.Port = "3306\n" }, "", driver.InvalidCredentials},
		{"URI only", func(c *driver.ConnectionDetails) {
			*c = driver.ConnectionDetails{URI: "mysql://u:credential-sentinel@host/app"}
		}, "", driver.UnsupportedConnection},
		{"URI instead of password", func(c *driver.ConnectionDetails) { c.Password = ""; c.URI = "mysql://u:credential-sentinel@host/app" }, "", driver.UnsupportedConnection},
		{"missing database", func(c *driver.ConnectionDetails) { c.Database = "" }, "", driver.InvalidOptions},
		{"URI without path", func(c *driver.ConnectionDetails) { c.Database = ""; c.URI = "mysql://host" }, "", driver.InvalidOptions},
		{"URI empty path", func(c *driver.ConnectionDetails) { c.Database = ""; c.URI = "mysql://host/" }, "", driver.InvalidOptions},
		{"URI query is not database", func(c *driver.ConnectionDetails) { c.Database = ""; c.URI = "mysql://host/?database=app" }, "", driver.InvalidOptions},
		{"URI nested path", func(c *driver.ConnectionDetails) { c.Database = ""; c.URI = "mysql://host/a/b" }, "", driver.InvalidCredentials},
		{"URI escaped slash", func(c *driver.ConnectionDetails) { c.Database = ""; c.URI = "mysql://host/a%2Fb" }, "", driver.InvalidCredentials},
		{"URI invalid escape", func(c *driver.ConnectionDetails) { c.Database = ""; c.URI = "mysql://host/%GG" }, "", driver.InvalidCredentials},
		{"URI missing host", func(c *driver.ConnectionDetails) { c.Database = ""; c.URI = "mysql:///app" }, "", driver.InvalidCredentials},
		{"URI empty hostname", func(c *driver.ConnectionDetails) { c.Database = ""; c.URI = "mysql://:3306/app" }, "", driver.InvalidCredentials},
		{"URI unsupported scheme", func(c *driver.ConnectionDetails) { c.Database = ""; c.URI = "postgresql://host/app" }, "", driver.InvalidCredentials},
		{"URI control character", func(c *driver.ConnectionDetails) { c.Database = ""; c.URI = "mysql://host/app\n" }, "", driver.InvalidCredentials},
		{"URI decoded newline", func(c *driver.ConnectionDetails) { c.Database = ""; c.URI = "mysql://host/app%0A" }, "", driver.InvalidCredentials},
		{"requested newline", func(*driver.ConnectionDetails) {}, "app\n", driver.InvalidOptions},
		{"requested NUL", func(*driver.ConnectionDetails) {}, "app\x00", driver.InvalidOptions},
		{"requested unsupported quotes", func(*driver.ConnectionDetails) {}, `both'" quotes`, driver.InvalidOptions},
		{"requested overlong database", func(*driver.ConnectionDetails) {}, strings.Repeat("x", 255), driver.InvalidOptions},
	} {
		t.Run(test.name, func(t *testing.T) {
			details := validConnectionDetails()
			test.change(&details)
			got, err := resolveConnection(test.requested, details)
			assertErrorKind(t, err, test.kind)
			if got != (resolvedConnection{}) {
				t.Fatal("invalid credentials produced a usable connection")
			}
			if strings.Contains(err.Error(), "credential-sentinel") || (details.URI != "" && strings.Contains(err.Error(), details.URI)) {
				t.Fatal("connection error exposed submitted credentials")
			}
		})
	}
}

func TestResolveConnectionPreservesConnectionValues(t *testing.T) {
	for _, test := range []struct {
		port     string
		wantPort int
		password string
	}{
		{"1", 1, "  credential-sentinel  "},
		{"65535", 65535, " \t "},
		{" 03306 ", 3306, "credential-sentinel"},
	} {
		t.Run(test.port, func(t *testing.T) {
			details := validConnectionDetails()
			details.Host, details.Username = " mysql.example.com ", " sbuser "
			details.Port, details.Password = test.port, test.password
			got, err := resolveConnection("", details)
			if err != nil || got.Host != details.Host || got.Username != details.Username || got.Password != details.Password || got.Port != test.wantPort {
				t.Fatalf("connection bytes changed or port was not normalized: %v", err)
			}
		})
	}
}

func TestResolveConnectionMatchesRunnerEncoding(t *testing.T) {
	for _, field := range []struct {
		name, option string
		set          func(*driver.ConnectionDetails, string)
	}{
		{"host", "mysql-host", func(c *driver.ConnectionDetails, v string) { c.Host = v }},
		{"database", "mysql-db", func(c *driver.ConnectionDetails, v string) { c.Database = v }},
		{"user", "mysql-user", func(c *driver.ConnectionDetails, v string) { c.Username = v }},
		{"password", "mysql-password", func(c *driver.ConnectionDetails, v string) { c.Password = v }},
	} {
		for i, value := range []string{
			"simple", " spaces \t ", "with'single", `with"double`, `both'"quotes`,
			`back\slash`, `trailing\`, "unicode-λ", "#hash=equals",
		} {
			t.Run(field.name+"/supported/"+strconv.Itoa(i), func(t *testing.T) {
				details := validConnectionDetails()
				field.set(&details, value)
				if _, err := resolveConnection("", details); err != nil {
					t.Fatalf("runner-supported value rejected: %v", err)
				}
			})
		}
		for i, value := range []string{
			"credential-sentinel\nmysql-debug=on", "credential-sentinel\r", "credential-sentinel\x00",
			`both'" quotes`, `both'",comma`, `'"leading`, `#both'"`,
			strings.Repeat("credential-sentinel", 30),
		} {
			t.Run(field.name+"/unsupported/"+strconv.Itoa(i), func(t *testing.T) {
				details := validConnectionDetails()
				field.set(&details, value)
				got, err := resolveConnection("", details)
				assertErrorKind(t, err, driver.InvalidCredentials)
				if got != (resolvedConnection{}) || strings.Contains(err.Error(), "credential-sentinel") || strings.Contains(err.Error(), value) {
					t.Fatal("unsupported connection value was not rejected safely")
				}
			})
		}
		// Quoted, unquoted, and multibyte values use the runner's byte limit.
		for _, test := range []struct{ name, prefix string }{{"quoted", ""}, {"unquoted", `both'"`}, {"multibyte", "λ"}} {
			t.Run(field.name+"/line-limit/"+test.name, func(t *testing.T) {
				quotes := 2
				if test.name == "unquoted" {
					quotes = 0
				}
				maximum := 254 - len(field.option) - 2 - quotes
				value := test.prefix + strings.Repeat("x", maximum-len(test.prefix))
				details := validConnectionDetails()
				field.set(&details, value)
				if _, err := resolveConnection("", details); err != nil {
					t.Fatalf("maximum supported line rejected: %v", err)
				}
				field.set(&details, value+"x")
				_, err := resolveConnection("", details)
				assertErrorKind(t, err, driver.InvalidCredentials)
			})
		}
	}
}
