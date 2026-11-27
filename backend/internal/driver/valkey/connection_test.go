package valkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

func TestSupportsTarget(t *testing.T) {
	for _, tt := range []struct {
		target driver.TargetInfo
		want   bool
	}{
		{driver.TargetInfo{DatabaseType: "valkey", Provider: "valkey"}, true},
		{driver.TargetInfo{DatabaseType: "valkey", Provider: "other"}, false},
		{driver.TargetInfo{DatabaseType: "redis", Provider: "valkey"}, false},
	} {
		if got := supportsTarget(tt.target); got != tt.want {
			t.Errorf("supportsTarget(%+v) = %t, want %t", tt.target, got, tt.want)
		}
	}
}

func TestResolveConnection(t *testing.T) {
	cert, key := testCertificatePair(t)
	// Match provider-valkey v0.2.0, including query-escaped URI userinfo.
	plaintext := driver.ConnectionDetails{
		Host: "cache-primary.default.svc", Port: "6379", Username: "default", Password: "test pass+word",
		URI: "redis://default:test+pass%2Bword@cache-primary.default.svc:6379",
	}
	tlsDetails := plaintext
	tlsDetails.URI = "rediss" + plaintext.URI[len("redis"):]
	tlsDetails.CACertificate = cert
	mtlsDetails := tlsDetails
	mtlsDetails.ClientCertificate, mtlsDetails.ClientKey = cert, key

	for _, tt := range []struct {
		name    string
		details driver.ConnectionDetails
		wantTLS bool
	}{
		{"plaintext", plaintext, false},
		{"tls", tlsDetails, true},
		{"mutual tls", mtlsDetails, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveConnection("0", tt.details)
			if err != nil {
				t.Fatal(err)
			}
			want := resolvedConnection{
				Host: tt.details.Host, Port: tt.details.Port, Username: tt.details.Username, Password: tt.details.Password,
				TLS: tt.wantTLS, CACertificate: tt.details.CACertificate,
				ClientCertificate: tt.details.ClientCertificate, ClientKey: tt.details.ClientKey,
			}
			if got != want {
				t.Fatal("resolved connection did not preserve the published settings")
			}
		})
	}
}

func TestResolveConnectionSSLSetting(t *testing.T) {
	ca, _ := testCertificatePair(t)
	for _, tc := range []struct {
		scheme, ssl string
		valid       bool
	}{
		{"redis", "false", true}, {"rediss", "true", true},
		{"redis", "true", false}, {"rediss", "false", false},
		{"redis", "", false}, {"rediss", "invalid", false},
	} {
		t.Run(tc.scheme+"/"+tc.ssl, func(t *testing.T) {
			details := driver.ConnectionDetails{Host: "cache", Port: "6379", Username: "default",
				Password: "secret-sentinel", URI: tc.scheme + "://cache:6379", SSL: &tc.ssl}
			if tc.scheme == "rediss" {
				details.CACertificate = ca
			}
			_, err := resolveConnection("", details)
			if !tc.valid {
				checkConnectionError(t, err, driver.InvalidCredentials)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResolveConnectionRejectsInvalidInput(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*driver.ConnectionDetails)
	}{
		{"missing URI", func(d *driver.ConnectionDetails) { d.URI = "" }},
		{"unknown scheme", func(d *driver.ConnectionDetails) { d.URI = "http://cache:6379" }},
		{"control in URI", func(d *driver.ConnectionDetails) { d.URI += "\n" }},
		{"bad host", func(d *driver.ConnectionDetails) { d.Host = "cache/path" }},
		{"different host", func(d *driver.ConnectionDetails) { d.Host = "another-cache" }},
		{"bad port", func(d *driver.ConnectionDetails) { d.Port = "65536" }},
		{"different port", func(d *driver.ConnectionDetails) { d.Port = "6380" }},
		{"invalid ACL username", func(d *driver.ConnectionDetails) { d.Username = "default:other" }},
		{"missing password", func(d *driver.ConnectionDetails) { d.Password = "" }},
		{"invalid password", func(d *driver.ConnectionDetails) { d.Password += "\x00" }},
		{"wrong database field", func(d *driver.ConnectionDetails) { d.Database = "1" }},
		{"wrong URI database", func(d *driver.ConnectionDetails) { d.URI += "/1" }},
		{"unexpected URI query", func(d *driver.ConnectionDetails) { d.URI += "?db=1" }},
		{"TLS without CA", func(d *driver.ConnectionDetails) { d.URI = "rediss://cache:6379" }},
		{"plaintext with TLS material", func(d *driver.ConnectionDetails) { d.CACertificate = "secret-sentinel" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			details := driver.ConnectionDetails{
				Host: "cache", Port: "6379", Username: "default", Password: "secret-sentinel",
				URI: "redis://default:secret-sentinel@cache:6379",
			}
			tt.mutate(&details)
			_, err := resolveConnection("", details)
			checkConnectionError(t, err, driver.InvalidCredentials)
		})
	}
	_, err := resolveConnection("1", driver.ConnectionDetails{})
	checkConnectionError(t, err, driver.InvalidOptions)
}

func TestResolveConnectionRejectsInvalidMTLS(t *testing.T) {
	cert, key := testCertificatePair(t)
	_, otherKey := testCertificatePair(t)
	for _, keys := range []struct{ cert, key string }{{cert, ""}, {"", key}, {cert, otherKey}} {
		_, err := resolveConnection("", driver.ConnectionDetails{
			Host: "cache", Port: "6379", Username: "default", Password: "secret-sentinel", URI: "rediss://cache:6379",
			CACertificate: cert, ClientCertificate: keys.cert, ClientKey: keys.key,
		})
		checkConnectionError(t, err, driver.InvalidCredentials)
	}
}

func checkConnectionError(t *testing.T, err error, kind driver.ErrorKind) {
	t.Helper()
	var driverErr *driver.Error
	if !errors.As(err, &driverErr) || driverErr.Kind != kind {
		t.Fatalf("expected %v error, got %T", kind, err)
	}
	if strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatal("connection error exposed a credential")
	}
}

func testCertificatePair(t *testing.T) (string, string) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotAfter: time.Now().Add(time.Hour),
		BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}
