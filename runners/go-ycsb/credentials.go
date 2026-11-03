package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/magiconair/properties"
)

// connectionConfig uses the same properties format and expansion rules as
// go-ycsb v1.0.3. Reject any value that cannot be read back unchanged.
func connectionConfig(c config) ([]byte, error) {
	invalid := errors.New("MONGODB_URI cannot be represented in the go-ycsb properties file; percent-encode reserved credential characters")
	if c.uri == "" || strings.ContainsFunc(c.uri, unicode.IsControl) || strings.Contains(c.uri, "${") {
		return nil, invalid
	}
	// Backslashes introduce properties-file escapes, even inside a URI.
	data := []byte("mongodb.url=" + strings.ReplaceAll(c.uri, `\`, `\\`) + "\n")
	// Read the generated text with the same parser go-ycsb uses, to check
	// that properties-file escaping preserves the exact URI and credentials.
	decoded, err := properties.Load(data, properties.UTF8)
	if err != nil || decoded.Len() != 1 || decoded.GetString("mongodb.url", "") != c.uri {
		return nil, invalid
	}
	return data, nil
}

func writeConnectionConfig(c config) (path string, cleanup func(), err error) {
	data, err := connectionConfig(c)
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "go-ycsb-")
	if err != nil {
		return "", nil, errors.New("could not create the private go-ycsb configuration directory")
	}
	path = filepath.Join(dir, "connection.properties")
	cleanup = func() {
		_ = os.Remove(path)
		_ = os.Remove(dir)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		cleanup()
		return "", nil, errors.New("could not create the private go-ycsb configuration file")
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		cleanup()
		return "", nil, errors.New("could not write the private go-ycsb configuration file")
	}
	return path, cleanup, nil
}
