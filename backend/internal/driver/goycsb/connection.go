package goycsb

import (
	"crypto/x509"
	"net/url"
	"strings"
	"unicode"

	"github.com/magiconair/properties"
	"github.com/openeverest/plugin-bench/backend/internal/driver"
	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
)

// resolvedConnection contains credentials for execution and redaction only.
// Never log it or include it in a run snapshot.
type resolvedConnection struct {
	Database, URI, OriginalURI, Password string
	CACertificate                        string
}

func resolveConnection(requestedDatabase string, details driver.ConnectionDetails) (resolvedConnection, error) {
	database := strings.TrimSpace(requestedDatabase)
	if strings.ContainsFunc(requestedDatabase, unicode.IsControl) || (database != "" && database != benchmarkDatabase) {
		return resolvedConnection{}, invalidOptions("go-ycsb supports only the ycsb database")
	}
	if details.URI == "" {
		return resolvedConnection{}, invalidCredentials("MongoDB connection URI is required")
	}
	if !strings.HasPrefix(details.URI, "mongodb://") {
		return resolvedConnection{}, &driver.Error{Kind: driver.UnsupportedConnection, Message: "go-ycsb requires a mongodb:// connection URI"}
	}
	if err := validateConnectionEncoding(details.URI); err != nil {
		return resolvedConnection{}, err
	}
	original, err := connstring.ParseAndValidate(details.URI)
	if err != nil || len(original.Hosts) == 0 {
		return resolvedConnection{}, invalidCredentials("MongoDB connection URI is invalid")
	}
	uri, err := url.Parse(details.URI)
	if err != nil || uri.Host == "" || uri.Fragment != "" {
		return resolvedConnection{}, invalidCredentials("MongoDB connection URI is invalid")
	}
	// The provider's URI path can select the authentication database (e.g.
	// /admin). Freeze that effective authSource before changing the target path.
	if !original.AuthSourceSet && original.AuthSource != "" {
		if uri.RawQuery != "" {
			uri.RawQuery += "&"
		}
		uri.RawQuery += "authSource=" + url.QueryEscape(original.AuthSource)
	}
	uri.Path, uri.RawPath = "/"+benchmarkDatabase, ""
	effectiveURI := uri.String()
	effective, err := connstring.ParseAndValidate(effectiveURI)
	if err != nil || effective.Database != benchmarkDatabase || effective.AuthSource != original.AuthSource ||
		effective.Username != original.Username || effective.Password != original.Password {
		return resolvedConnection{}, invalidCredentials("MongoDB authentication settings could not be preserved")
	}
	if err := validateConnectionEncoding(effectiveURI); err != nil {
		return resolvedConnection{}, err
	}
	if details.CACertificate != "" && !x509.NewCertPool().AppendCertsFromPEM([]byte(details.CACertificate)) {
		return resolvedConnection{}, invalidCredentials("MongoDB TLS CA certificate is invalid")
	}
	return resolvedConnection{
		Database: benchmarkDatabase, URI: effectiveURI, OriginalURI: details.URI, Password: original.Password,
		CACertificate: details.CACertificate,
	}, nil
}

// Match the runner's properties-file encoding without writing a file. The
// upstream properties parser must recover exactly the URI supplied to it.
func validateConnectionEncoding(raw string) error {
	invalid := func() error {
		return invalidCredentials("MongoDB connection URI cannot be represented in the runner properties file")
	}
	if strings.ContainsFunc(raw, unicode.IsControl) || strings.Contains(raw, "${") {
		return invalid()
	}
	data := []byte("mongodb.url=" + strings.ReplaceAll(raw, `\`, `\\`) + "\n")
	decoded, err := properties.Load(data, properties.UTF8)
	if err != nil || decoded.Len() != 1 || decoded.GetString("mongodb.url", "") != raw {
		return invalid()
	}
	return nil
}

func invalidCredentials(message string) error {
	return &driver.Error{Kind: driver.InvalidCredentials, Message: message}
}
