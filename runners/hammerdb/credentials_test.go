package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWriteScriptsPrivateFilesAndCleanup(t *testing.T) {
	for _, engine := range []string{"postgresql", "mysql"} {
		for _, initialize := range []bool{false, true} {
			env := validEnvironment(engine)
			c, err := loadConfig(func(key string) string { return env[key] })
			if err != nil {
				t.Fatal(err)
			}
			c.initialize = initialize
			paths, cleanup, err := writeScripts(c)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			for _, path := range []string{paths.directory, paths.state} {
				info, err := os.Stat(path)
				if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
					t.Fatal("temporary directory is not private")
				}
			}
			files := map[string]bool{paths.run: false}
			if initialize {
				files[paths.prepare] = true
			} else if paths.prepare != "" {
				t.Fatal("reuse unexpectedly staged a preparation script")
			}
			for path, prepare := range files {
				info, err := os.Stat(path)
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
					t.Fatal("script is not a private regular file")
				}
				data, err := os.ReadFile(path)
				completion := paths.runDone
				if prepare {
					completion = paths.prepareDone
				}
				want, generationErr := phaseScript(c, prepare, completion)
				if err != nil || generationErr != nil || string(data) != want {
					t.Fatal("staged script differs from the generated configuration")
				}
			}
			// SQLite state, journals and logs created later must also be removed.
			if err := os.WriteFile(filepath.Join(paths.state, "hammer.DB-journal"), []byte("private state"), 0600); err != nil {
				t.Fatal(err)
			}
			cleanup()
			cleanup()
			if _, err := os.Stat(paths.directory); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("cleanup left temporary material behind")
			}
		}
	}
}

func TestWriteScriptsIsolation(t *testing.T) {
	c, err := loadConfig(func(key string) string { return validEnvironment("postgresql")[key] })
	if err != nil {
		t.Fatal(err)
	}
	first, cleanupFirst, err := writeScripts(c)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupFirst()
	second, cleanupSecond, err := writeScripts(c)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupSecond()
	if first.directory == second.directory || first.state == second.state {
		t.Fatal("separate invocations shared writable state")
	}
	cleanupFirst()
	if _, err := os.Stat(second.run); err != nil {
		t.Fatal("one invocation's cleanup removed another's script")
	}
}

func TestWriteScriptsFailure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	if paths, cleanup, err := writeScripts(config{databaseType: "unsupported"}); err == nil || paths != (scriptPaths{}) || cleanup != nil {
		t.Fatal("generation failure returned usable paths")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("generation failure left temporary files")
	}
	t.Setenv("TMPDIR", filepath.Join(root, "missing"))
	c, err := loadConfig(func(key string) string { return validEnvironment("mysql")[key] })
	if err != nil {
		t.Fatal(err)
	}
	if paths, cleanup, err := writeScripts(c); err == nil || paths != (scriptPaths{}) || cleanup != nil || strings.Contains(err.Error(), root) {
		t.Fatal("directory failure returned paths or exposed filesystem details")
	}
}

func TestWriteScriptsRejectsUnsupportedPasswords(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	for _, engine := range []string{"postgresql", "mysql"} {
		for _, initialize := range []bool{false, true} {
			for _, password := range []string{" secret", "secret\t", `ab"cd`} {
				c, err := loadConfig(func(key string) string { return validEnvironment(engine)[key] })
				if err != nil {
					t.Fatal(err)
				}
				// Bypass loadConfig to ensure script staging also rejects these
				// credentials before writing either phase's configuration.
				c.password, c.initialize = password, initialize
				if paths, cleanup, err := writeScripts(c); err == nil || paths != (scriptPaths{}) || cleanup != nil || !strings.Contains(err.Error(), "DB_PASSWORD") {
					t.Fatal("unsupported password produced staged scripts")
				}
			}
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("unsupported password left private files behind")
	}
}

func TestWritePrivateFileExclusive(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "existing")
	if err := writePrivateFile(path, []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(path, []byte("replacement")); !errors.Is(err, os.ErrExist) {
		t.Fatal("private writer overwrote an existing file")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(link, []byte("replacement")); !errors.Is(err, os.ErrExist) {
		t.Fatal("private writer followed a symlink")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original" {
		t.Fatal("existing file contents changed")
	}
}

func TestScriptEnvironment(t *testing.T) {
	base := []string{"PATH=/bin", "TMP=/shared", "TMPDIR=/shared", "TEMP=/shared", "HOME=/shared", "TMP=/duplicate", "DB_PASSWORD=private", "OTHER=a=b"}
	original := append([]string(nil), base...)
	paths := scriptPaths{state: "/private/state"}
	want := []string{"PATH=/bin", "OTHER=a=b", "TMP=/private/state", "TMPDIR=/private/state", "TEMP=/private/state", "HOME=/private/state"}
	if got := paths.environment(base); !reflect.DeepEqual(got, want) {
		t.Fatal("child environment retained shared state paths or lost unrelated settings")
	}
	if !reflect.DeepEqual(base, original) {
		t.Fatal("child environment changed the parent's environment slice")
	}
}
