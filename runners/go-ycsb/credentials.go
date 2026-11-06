package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/magiconair/properties"
)

const (
	urlProperty       = "mongodb.url"
	tlsCAFileProperty = "mongodb.tls_ca_file"
)

var errUnrepresentableURI = errors.New("MONGODB_URI cannot be represented in the go-ycsb properties file; percent-encode reserved credential characters")

// connectionConfig uses the same properties format and expansion rules as
// go-ycsb v1.0.3. Reject any value that cannot be read back unchanged.
func connectionConfig(c config, caFilePath string) ([]byte, error) {
	if !representableProperty(c.uri) {
		return nil, errUnrepresentableURI
	}
	values := [][2]string{{urlProperty, c.uri}}
	if caFilePath != "" {
		if !representableProperty(caFilePath) {
			return nil, errors.New("the private CA file path cannot be represented in the go-ycsb properties file")
		}
		values = append(values, [2]string{tlsCAFileProperty, caFilePath})
	}
	var data []byte
	for _, value := range values {
		// Backslashes introduce properties-file escapes, even inside a URI.
		data = append(data, value[0]+"="+strings.ReplaceAll(value[1], `\`, `\\`)+"\n"...)
	}
	// Read the generated text with the same parser go-ycsb uses, to check
	// that properties-file escaping preserves the exact URI and credentials.
	decoded, err := properties.Load(data, properties.UTF8)
	if err != nil || decoded.Len() != len(values) {
		return nil, errUnrepresentableURI
	}
	for _, value := range values {
		if decoded.GetString(value[0], "") != value[1] {
			return nil, errUnrepresentableURI
		}
	}
	return data, nil
}

func representableProperty(value string) bool {
	return value != "" && !strings.ContainsFunc(value, unicode.IsControl) && !strings.Contains(value, "${")
}

// writeConnectionConfig writes the properties file, and the CA it references
// when one is configured, into a private directory removed by cleanup.
func writeConnectionConfig(c config) (path string, cleanup func(), err error) {
	// Validate before creating anything so encoding failures leave no files.
	if _, err := connectionConfig(c, ""); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "go-ycsb-")
	if err != nil {
		return "", nil, errors.New("could not create the private go-ycsb configuration directory")
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	var caPath string
	if c.tlsCA != "" {
		caPath = filepath.Join(dir, "ca.pem")
		if err := writePrivateFile(caPath, []byte(c.tlsCA)); err != nil {
			cleanup()
			return "", nil, errors.New("could not write the private go-ycsb CA file")
		}
	}
	data, err := connectionConfig(c, caPath)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	path = filepath.Join(dir, "connection.properties")
	if err := writePrivateFile(path, data); err != nil {
		cleanup()
		return "", nil, errors.New("could not write the private go-ycsb configuration file")
	}
	return path, cleanup, nil
}

func writePrivateFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Close())
}
