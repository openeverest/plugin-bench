package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		data, err := connectionConfig(config{uri: uri})
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
		data, err := connectionConfig(config{uri: value})
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
