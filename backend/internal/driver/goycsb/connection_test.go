package goycsb

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
)

func TestResolveConnectionPreservesAuthentication(t *testing.T) {
	for _, test := range []struct {
		name, uri, authSource string
	}{
		{"provider admin path", "mongodb://user:credential-sentinel@mongo/admin?ssl=false", "admin"},
		{"no path defaults to admin", "mongodb://user:credential-sentinel@mongo", "admin"},
		{"ycsb path", "mongodb://user:credential-sentinel@mongo/ycsb", "ycsb"},
		{"different authentication database", "mongodb://user:credential-sentinel@mongo/accounts", "accounts"},
		{"explicit authSource", "mongodb://user:credential-sentinel@mongo/admin?authSource=accounts", "accounts"},
		{"case insensitive option", "mongodb://user:credential-sentinel@mongo/admin?AUTHSOURCE=accounts", "accounts"},
		{"encoded credentials", "mongodb://user%40name:p%40ss%2B%25%3A%2Fword@mongo/admin", "admin"},
		{"replica set hosts", "mongodb://user:credential-sentinel@mongo1:27017,mongo2:27018/admin?replicaSet=rs0&tls=false&connectTimeoutMS=3000", "admin"},
		{"IPv6", "mongodb://user:credential-sentinel@[::1]:27017/admin", "admin"},
		{"semicolon options", "mongodb://user:credential-sentinel@mongo/admin?tls=false;connectTimeoutMS=3000", "admin"},
		{"external authentication", "mongodb://user:credential-sentinel@mongo/?authMechanism=PLAIN", "$external"},
		{"no authentication", "mongodb://mongo", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			details := driver.ConnectionDetails{URI: test.uri}
			got, err := resolveConnection("", details)
			if err != nil {
				t.Fatalf("valid connection rejected: %v", err)
			}
			original, err := connstring.ParseAndValidate(test.uri)
			if err != nil {
				t.Fatal("invalid test URI")
			}
			effective, err := connstring.ParseAndValidate(got.URI)
			if err != nil || got.Database != "ycsb" || effective.Database != "ycsb" || effective.AuthSource != test.authSource {
				t.Fatal("effective connection changed the database or authentication source incorrectly")
			}
			if effective.Username != original.Username || effective.Password != original.Password || got.Password != original.Password || got.OriginalURI != test.uri {
				t.Fatal("connection lost credentials or original URI for redaction")
			}
			before, _ := url.Parse(test.uri)
			after, _ := url.Parse(got.URI)
			if before.Host != after.Host || !strings.HasPrefix(after.RawQuery, before.RawQuery) {
				t.Fatal("connection changed hosts or existing URI options")
			}
		})
	}
}

func TestResolveConnectionUsesURIAndFixedDatabase(t *testing.T) {
	for _, requested := range []string{"", "ycsb", " ycsb "} {
		details := driver.ConnectionDetails{
			URI:  "mongodb://user:credential-sentinel@mongo/admin?authSource=admin",
			Host: "ignored", Port: "invalid", Username: "ignored", Password: "ignored", Database: "admin",
		}
		got, err := resolveConnection(requested, details)
		if err != nil || got.Database != "ycsb" || got.Password != "credential-sentinel" {
			t.Fatal("URI was not authoritative or fixed database was rejected")
		}
	}
}

func TestResolveConnectionRejectsUnsafeInput(t *testing.T) {
	for _, test := range []struct {
		name, requested, uri string
		kind                 driver.ErrorKind
	}{
		{"different requested database", "credential-sentinel", "mongodb://mongo", driver.InvalidOptions},
		{"database is case sensitive", "YCSB", "mongodb://mongo", driver.InvalidOptions},
		{"control in requested database", "ycsb\n", "mongodb://mongo", driver.InvalidOptions},
		{"missing URI", "", "", driver.InvalidCredentials},
		{"unsupported scheme", "", "https://credential-sentinel", driver.UnsupportedConnection},
		{"SRV URI", "", "mongodb+srv://credential-sentinel.invalid", driver.UnsupportedConnection},
		{"empty host", "", "mongodb:///ycsb", driver.InvalidCredentials},
		{"invalid port", "", "mongodb://u:credential-sentinel@mongo:99999/admin", driver.InvalidCredentials},
		{"invalid escape", "", "mongodb://u:credential-sentinel%GG@mongo/admin", driver.InvalidCredentials},
		{"empty authSource", "", "mongodb://u:credential-sentinel@mongo/admin?authSource=", driver.InvalidCredentials},
		{"conflicting TLS options", "", "mongodb://u:credential-sentinel@mongo/admin?tls=true&ssl=false", driver.InvalidCredentials},
		{"fragment", "", "mongodb://mongo/admin#credential-sentinel", driver.InvalidCredentials},
		{"control", "", "mongodb://u:credential-sentinel\n@mongo/admin", driver.InvalidCredentials},
		{"properties expansion", "", "mongodb://u:${credential-sentinel}@mongo/admin", driver.InvalidCredentials},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveConnection(test.requested, driver.ConnectionDetails{URI: test.uri})
			var problem *driver.Error
			if !errors.As(err, &problem) || problem.Kind != test.kind {
				t.Fatalf("error = %v; want kind %q", err, test.kind)
			}
			if got != (resolvedConnection{}) || strings.Contains(err.Error(), "credential-sentinel") {
				t.Fatal("invalid input returned credentials or exposed submitted data")
			}
		})
	}
}

func TestValidateConnectionEncoding(t *testing.T) {
	for _, raw := range []string{
		"mongodb://user:pa%5Css@mongo/ycsb?authSource=admin",
		`mongodb://user:pa\ss@mongo/ycsb?authSource=admin`,
		"mongodb://user:p%24%7Bword%7D@mongo/ycsb?authSource=admin",
	} {
		if err := validateConnectionEncoding(raw); err != nil {
			t.Fatalf("representable URI rejected: %v", err)
		}
	}
}
