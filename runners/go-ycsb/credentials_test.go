package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/magiconair/properties"
)

func TestConnectionConfigRoundTrip(t *testing.T) {
	for _, uri := range []string{
		"mongodb://user:password@localhost/ycsb?authSource=admin",
		"mongodb://user:p%40ss%3A%2F%23%20%24%7Bvalue%7D@localhost/ycsb?authSource=admin",
		"mongodb://user:pass=word!@localhost/ycsb?authSource=admin&appName=benchmark",
		`mongodb://user:pass\word@localhost/ycsb`,
		"mongodb://localhost/ycsb?appName=测试",
	} {
		data, err := connectionConfig(config{uri: uri}, "")
		if err != nil {
			t.Fatal("valid properties value was rejected")
		}
		p, err := properties.Load(data, properties.UTF8)
		if err != nil || p.Len() != 1 || p.GetString("mongodb.url", "") != uri {
			t.Fatal("properties decoding must preserve exactly one unchanged URI")
		}
	}
}

func TestConnectionConfigRejectsInjectionAndExpansion(t *testing.T) {
	for _, value := range []string{"", "mongodb://user:private-secret@localhost/ycsb\nthreadcount=100", "mongodb://localhost/ycsb\r", "mongodb://localhost/ycsb\x00", "mongodb://user:${private-secret}@localhost/ycsb"} {
		data, err := connectionConfig(config{uri: value}, "")
		if err == nil || len(data) != 0 || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("unsafe properties must fail without exposing credentials")
		}
	}
}

func TestPrivateConnectionFile(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("TMPDIR", parent)
	uri := validEnvironment()["MONGODB_URI"]
	path, cleanup, err := writeConnectionConfig(config{uri: uri})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{
		{path, 0600}, {filepath.Dir(path), 0700},
	} {
		info, err := os.Stat(item.path)
		if err != nil || info.Mode().Perm() != item.mode {
			t.Fatal("connection file and directory must be private")
		}
	}
	p, err := properties.LoadFile(path, properties.UTF8)
	if err != nil || p.Len() != 1 || p.GetString("mongodb.url", "") != uri {
		t.Fatal("private file must preserve the URI")
	}
	cleanup()
	cleanup() // Cleanup is safe to repeat, including from a deferred call.
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("cleanup must remove both the file and its private directory")
	}
}

func TestConnectionFileFailure(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("TMPDIR", parent)
	path, cleanup, err := writeConnectionConfig(config{uri: "${private-secret}"})
	entries, readErr := os.ReadDir(parent)
	if err == nil || path != "" || cleanup != nil || readErr != nil || len(entries) != 0 {
		t.Fatal("encoding failure must not create temporary files")
	}
	t.Setenv("TMPDIR", filepath.Join(parent, "missing"))
	path, cleanup, err = writeConnectionConfig(config{uri: validEnvironment()["MONGODB_URI"]})
	if err == nil || path != "" || cleanup != nil || strings.Contains(err.Error(), parent) {
		t.Fatal("file errors must return a safe diagnostic without exposing paths")
	}
}

func testCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestConnectionConfigReferencesCAFile(t *testing.T) {
	uri := validEnvironment()["MONGODB_URI"]
	data, err := connectionConfig(config{uri: uri}, `/tmp/go-ycsb-1/ca\.pem`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := properties.Load(data, properties.UTF8)
	if err != nil || p.Len() != 2 || p.GetString("mongodb.url", "") != uri ||
		p.GetString("mongodb.tls_ca_file", "") != `/tmp/go-ycsb-1/ca\.pem` {
		t.Fatal("properties must preserve both the URI and the CA file path")
	}
	for _, path := range []string{"/tmp/${HOME}/ca.pem", "/tmp/ca.pem\nthreadcount=100"} {
		if _, err := connectionConfig(config{uri: uri}, path); err == nil {
			t.Fatal("unrepresentable CA file paths must be rejected")
		}
	}
}

func TestPrivateCAFile(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("TMPDIR", parent)
	ca := testCAPEM(t)
	path, cleanup, err := writeConnectionConfig(config{uri: validEnvironment()["MONGODB_URI"], tlsCA: ca})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	p, err := properties.LoadFile(path, properties.UTF8)
	if err != nil || p.Len() != 2 {
		t.Fatal("connection file must reference the CA file")
	}
	caPath := p.GetString("mongodb.tls_ca_file", "")
	if filepath.Dir(caPath) != filepath.Dir(path) {
		t.Fatal("CA file must live in the private connection directory")
	}
	info, err := os.Stat(caPath)
	data, readErr := os.ReadFile(caPath)
	if err != nil || info.Mode().Perm() != 0600 || readErr != nil || string(data) != ca {
		t.Fatal("CA file must be private and contain the configured certificate")
	}
	cleanup()
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("cleanup must remove the CA file with the private directory")
	}
}
