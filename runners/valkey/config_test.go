package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func valkeyTestEnv() map[string]string {
	return map[string]string{
		"VALKEY_HOST": "valkey.test", "VALKEY_PORT": "6379",
		"VALKEY_USERNAME": "default", "VALKEY_PASSWORD": "test-password",
		"VALKEY_TLS": "false",
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	env := valkeyTestEnv()
	delete(env, "VALKEY_TLS")
	env["VALKEY_TLS_CA"] = caPEM
	got, err := loadConfig(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	want := config{
		host: "valkey.test", port: 6379, username: "default", password: "test-password",
		tls: true, tlsCA: caPEM, durationSeconds: 30, threads: 2, clientsPerThread: 10,
		keyCount: 10_000, valueSizeBytes: 256,
	}
	if got != want {
		t.Fatal("configuration did not match the documented defaults and connection settings")
	}
}

func TestLoadConfigAcceptsCombinedLimits(t *testing.T) {
	env := valkeyTestEnv()
	env["BENCH_THREADS"], env["BENCH_CLIENTS_PER_THREAD"] = "8", "16"
	env["BENCH_KEY_COUNT"], env["BENCH_VALUE_SIZE"] = "4096", "16384"
	env["BENCH_DURATION"] = "300"
	got, err := loadConfig(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	if got.tls || got.threads != 8 || got.clientsPerThread != 16 ||
		got.keyCount != 4096 || got.valueSizeBytes != 16384 || got.durationSeconds != 300 {
		t.Fatal("configuration did not preserve explicit settings at the limits")
	}
}

func TestLoadConfigRejectsInvalidSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"host with port", map[string]string{"VALKEY_HOST": "valkey.test:6379"}, "VALKEY_HOST must"},
		{"invalid port", map[string]string{"VALKEY_PORT": "65536"}, "VALKEY_PORT must"},
		{"missing username", map[string]string{"VALKEY_USERNAME": ""}, "VALKEY_USERNAME is required"},
		{"username control character", map[string]string{"VALKEY_USERNAME": "test-user-sensitive\r"}, "VALKEY_USERNAME is required"},
		{"username with colon", map[string]string{"VALKEY_USERNAME": "test:user"}, "VALKEY_USERNAME must not contain a colon"},
		{"missing password", map[string]string{"VALKEY_PASSWORD": ""}, "VALKEY_PASSWORD is required"},
		{"password control character", map[string]string{"VALKEY_PASSWORD": "test-password\n"}, "VALKEY_PASSWORD is required"},
		{"invalid UTF-8", map[string]string{"VALKEY_PASSWORD": "test-password\xff"}, "VALKEY_PASSWORD is required"},
		{"invalid TLS flag", map[string]string{"VALKEY_TLS": "yes"}, "VALKEY_TLS must"},
		{"CA without TLS", map[string]string{"VALKEY_TLS_CA": "unused-ca"}, "VALKEY_TLS_CA requires VALKEY_TLS=true"},
		{"TLS without CA", map[string]string{"VALKEY_TLS": "true"}, "VALKEY_TLS_CA must contain"},
		{"invalid CA", map[string]string{"VALKEY_TLS": "true", "VALKEY_TLS_CA": "invalid-pem"}, "VALKEY_TLS_CA must contain"},
		{"zero threads", map[string]string{"BENCH_THREADS": "0"}, "BENCH_THREADS must"},
		{"too many clients", map[string]string{"BENCH_THREADS": "8", "BENCH_CLIENTS_PER_THREAD": "17"}, "must not exceed 128 total clients"},
		{"seed too large", map[string]string{"BENCH_KEY_COUNT": "4097", "BENCH_VALUE_SIZE": "16384"}, "must not exceed 67108864 bytes"},
		{"duration too long", map[string]string{"BENCH_DURATION": "301"}, "BENCH_DURATION must"},
		{"too many keys", map[string]string{"BENCH_KEY_COUNT": "100001"}, "BENCH_KEY_COUNT must"},
		{"value too large", map[string]string{"BENCH_VALUE_SIZE": "16385"}, "BENCH_VALUE_SIZE must"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := valkeyTestEnv()
			for name, value := range tc.env {
				env[name] = value
			}
			_, err := loadConfig(func(name string) string { return env[name] })
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if strings.Contains(err.Error(), "test-password") || strings.Contains(err.Error(), "test-user-sensitive") {
				t.Fatal("configuration error includes credentials")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestValidHost(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"valkey-0.default.svc.", true},
		{"127.0.0.1", true},
		{"2001:db8::1", true},
		{"", false},
		{".", false},
		{"valkey:6379", false},
		{"[::1]", false},
		{"[::1]:6379", false},
		{"2001:::1", false},
		{"valkey..svc", false},
		{"-valkey.svc", false},
		{"valkey-.svc", false},
		{"val_key.svc", false},
		{"valkey\n.svc", false},
		{"valkey .svc", false},
		{"valkey/svc", false},
		{strings.Repeat("x", 63), true},
		{strings.Repeat("x", 64), false},
		{strings.Repeat(strings.Repeat("x", 63)+".", 3) + strings.Repeat("x", 61), true},
		{strings.Repeat(strings.Repeat("x", 63)+".", 3) + strings.Repeat("x", 62), false},
	} {
		if got := validHost(tc.host); got != tc.want {
			t.Errorf("validHost(%q) = %t, want %t", tc.host, got, tc.want)
		}
	}
}
