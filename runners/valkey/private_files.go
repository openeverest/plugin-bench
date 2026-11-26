package main

import (
	"errors"
	"os"
	"path/filepath"
)

type privateFiles struct {
	caPath     string
	resultPath string
}

// createPrivateFiles stores the provider CA and Memtier's raw JSON output in a
// per-run directory. MkdirTemp creates the directory with mode 0700; files are
// created with mode 0600. The caller must invoke cleanup after the run.
func createPrivateFiles(c config) (privateFiles, func(), error) {
	dir, err := os.MkdirTemp("", "valkey-runner-")
	if err != nil {
		return privateFiles{}, nil, errors.New("could not create the private Valkey runner directory")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	files := privateFiles{
		resultPath: filepath.Join(dir, "memtier.json"),
	}

	if c.tls {
		files.caPath = filepath.Join(dir, "ca.pem")
		if err := writePrivateFile(files.caPath, []byte(c.tlsCA)); err != nil {
			cleanup()
			return privateFiles{}, nil, errors.New("could not write the private Valkey CA file")
		}
	}
	if err := writePrivateFile(files.resultPath, nil); err != nil {
		cleanup()
		return privateFiles{}, nil, errors.New("could not create the private Memtier result file")
	}
	return files, cleanup, nil
}

func writePrivateFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Close())
}
