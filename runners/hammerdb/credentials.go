package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type scriptPaths struct {
	directory   string
	prepare     string
	run         string
	state       string
	prepareDone string
	runDone     string
}

// writeScripts stages configuration text, including credentials, in a private
// directory. The caller must defer cleanup after success. Each script includes
// preflight and worker checks.
func writeScripts(c config) (scriptPaths, func(), error) {
	if _, err := configurationScript(c, false); err != nil {
		return scriptPaths{}, nil, err
	}
	dir, err := os.MkdirTemp("", "hammerdb-")
	if err != nil {
		return scriptPaths{}, nil, errors.New("could not create the private HammerDB directory")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	// Keep paths absolute even when the caller's TMPDIR was relative.
	abs, err := filepath.Abs(dir)
	if err != nil {
		cleanup()
		return scriptPaths{}, nil, errors.New("could not resolve the private HammerDB directory")
	}
	dir = abs
	paths := scriptPaths{directory: dir, run: filepath.Join(dir, "run.tcl"), state: filepath.Join(dir, "state")}
	paths.runDone = filepath.Join(dir, "run.done")
	run, err := phaseScript(c, false, paths.runDone)
	if err != nil {
		cleanup()
		return scriptPaths{}, nil, err
	}
	if err := os.Mkdir(paths.state, 0700); err != nil {
		cleanup()
		return scriptPaths{}, nil, errors.New("could not create the private HammerDB state directory")
	}
	if c.databaseType == "mysql" {
		// mysqltcl loads the mysqltcl option group through libmysqlclient.
		// HOME points at this private state directory for both phase processes.
		if err := writePrivateFile(filepath.Join(paths.state, ".my.cnf"), []byte("[mysqltcl]\nconnect-timeout=10\n")); err != nil {
			cleanup()
			return scriptPaths{}, nil, errors.New("could not write the MySQL connection timeout settings")
		}
	}
	if err := writePrivateFile(paths.run, []byte(run)); err != nil {
		cleanup()
		return scriptPaths{}, nil, errors.New("could not write the private HammerDB run script")
	}
	if c.initialize {
		paths.prepare = filepath.Join(dir, "prepare.tcl")
		paths.prepareDone = filepath.Join(dir, "prepare.done")
		prepare, err := phaseScript(c, true, paths.prepareDone)
		if err != nil {
			cleanup()
			return scriptPaths{}, nil, err
		}
		if err := writePrivateFile(paths.prepare, []byte(prepare)); err != nil {
			cleanup()
			return scriptPaths{}, nil, errors.New("could not write the private HammerDB preparation script")
		}
	}
	return paths, cleanup, nil
}

func writePrivateFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Close())
}

// HammerDB 6.1.1's findtempdir checks TMP before TMPDIR on Linux. Override
// all three names so its default SQLite configuration/Jobs files and logs
// stay under this invocation's private directory. The password is already in
// the private script and need not be inherited by the child process.
func (p scriptPaths) environment(base []string) []string {
	result := make([]string, 0, len(base)+4)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "TMP", "TMPDIR", "TEMP", "HOME", "DB_PASSWORD":
			continue
		}
		result = append(result, entry)
	}
	for _, key := range []string{"TMP", "TMPDIR", "TEMP", "HOME"} {
		result = append(result, key+"="+p.state)
	}
	return result
}
