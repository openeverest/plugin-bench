package hammerdb

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

// resolvedConnection contains validated settings for the runner. Never log it.
type resolvedConnection struct {
	DatabaseType, Host, Database, Username, Password string
	Port                                             int
	URI, PGSSLMode                                   string
	MySQLSSL                                         bool
}

func resolveConnection(target driver.TargetInfo, requestedDatabase string, details driver.ConnectionDetails) (resolvedConnection, error) {
	databaseType := strings.ToLower(strings.TrimSpace(target.DatabaseType))
	if databaseType != "postgresql" && databaseType != "mysql" {
		return resolvedConnection{}, &driver.Error{Kind: driver.UnsupportedConnection, Message: "HammerDB requires a PostgreSQL or MySQL target"}
	}
	if strings.TrimSpace(details.Password) == "" && strings.TrimSpace(details.URI) != "" {
		return resolvedConnection{}, &driver.Error{Kind: driver.UnsupportedConnection, Message: "URI-only credentials are not supported by the HammerDB runner"}
	}
	var err error
	var uri *url.URL
	if strings.TrimSpace(details.URI) != "" {
		uri, err = parseConnectionURI(databaseType, details.URI)
		if err != nil {
			return resolvedConnection{}, err
		}
	}
	pgSSLMode, mysqlSSL, err := transportSettings(databaseType, uri)
	if err != nil {
		return resolvedConnection{}, err
	}
	if !validHost(details.Host) {
		return resolvedConnection{}, invalidCredentials("database host must be one IP address or DNS hostname")
	}
	if strings.TrimSpace(details.Username) == "" || !validName(details.Username, usernameLimit(databaseType)) {
		return resolvedConnection{}, invalidCredentials("database username is missing or unsupported by the HammerDB runner")
	}
	if !validPassword(details.Password) {
		return resolvedConnection{}, invalidCredentials("database password is unsupported by the HammerDB runner")
	}
	port, err := strconv.Atoi(strings.TrimSpace(details.Port))
	if err != nil || port < 1 || port > 65535 {
		return resolvedConnection{}, invalidCredentials("database port must be an integer between 1 and 65535")
	}

	database := strings.TrimSpace(requestedDatabase)
	if database != "" {
		if !validName(database, databaseLimit(databaseType)) {
			return resolvedConnection{}, invalidOptions("database name contains unsupported characters or exceeds the database limit")
		}
	} else {
		database = strings.TrimSpace(details.Database)
		if database == "" && uri != nil {
			database, err = databaseFromURI(uri)
			if err != nil {
				return resolvedConnection{}, err
			}
		}
		if database == "" {
			return resolvedConnection{}, invalidOptions("could not determine the instance's default database; enter a database name")
		}
		if !validName(database, databaseLimit(databaseType)) {
			return resolvedConnection{}, invalidCredentials("default database name is unsupported by the HammerDB runner")
		}
	}

	return resolvedConnection{
		DatabaseType: databaseType,
		Host:         details.Host,
		Port:         port,
		Database:     database,
		Username:     details.Username,
		Password:     details.Password,
		URI:          details.URI,
		PGSSLMode:    pgSSLMode,
		MySQLSSL:     mysqlSSL,
	}, nil
}

func parseConnectionURI(databaseType, raw string) (*url.URL, error) {
	if strings.ContainsAny(raw, "\x00\r\n") {
		return nil, invalidCredentials("connection URI contains unsupported control characters")
	}
	uri, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || uri.Hostname() == "" || !validDatabaseURIScheme(databaseType, uri.Scheme) {
		return nil, invalidCredentials("connection URI does not match the authorized database type")
	}
	return uri, nil
}

func databaseFromURI(uri *url.URL) (string, error) {
	// url.Parse decodes escaped path characters. A slash in the decoded path
	// means the URI does not unambiguously identify one database name.
	database := strings.TrimPrefix(uri.Path, "/")
	if strings.Contains(database, "/") {
		return "", invalidCredentials("connection URI must identify one database")
	}
	// An absent path is valid for providers without a default database. Let the
	// resolver ask the caller for a database name when no other hint exists.
	return strings.TrimSpace(database), nil
}

func transportSettings(databaseType string, uri *url.URL) (string, bool, error) {
	if databaseType == "postgresql" {
		mode := "prefer"
		if uri == nil {
			return mode, false, nil
		}
		values, err := url.ParseQuery(uri.RawQuery)
		if err != nil {
			return "", false, invalidCredentials("connection URI has malformed query parameters")
		}
		for key := range values {
			if strings.EqualFold(key, "tls") {
				return "", false, invalidCredentials("connection URI uses a TLS setting unsupported for PostgreSQL")
			}
			if isUnsupportedTLSOption(key) {
				return "", false, &driver.Error{Kind: driver.UnsupportedConnection, Message: "connection URI requests TLS settings unsupported by the HammerDB runner"}
			}
		}
		setting, present, err := uniqueQueryValue(values, "sslmode")
		if err != nil {
			return "", false, err
		}
		if present {
			switch setting {
			case "disable", "prefer", "require":
				mode = setting
			case "verify-ca", "verify-full":
				return "", false, &driver.Error{Kind: driver.UnsupportedConnection, Message: "PostgreSQL certificate verification is not supported by the HammerDB runner"}
			case "allow":
				return "", false, &driver.Error{Kind: driver.UnsupportedConnection, Message: "PostgreSQL sslmode allow is not supported by the HammerDB runner"}
			default:
				return "", false, invalidCredentials("PostgreSQL sslmode must be disable, prefer, or require")
			}
		}
		return mode, false, nil
	}

	if uri == nil {
		return "", true, nil
	}
	values, err := url.ParseQuery(uri.RawQuery)
	if err != nil {
		return "", false, invalidCredentials("connection URI has malformed query parameters")
	}
	for key := range values {
		if strings.EqualFold(key, "sslmode") {
			return "", false, invalidCredentials("connection URI uses a TLS setting unsupported for MySQL")
		}
		if isUnsupportedTLSOption(key) {
			return "", false, &driver.Error{Kind: driver.UnsupportedConnection, Message: "connection URI requests TLS settings unsupported by the HammerDB runner"}
		}
	}
	setting, present, err := uniqueQueryValue(values, "tls")
	if err != nil {
		return "", false, err
	}
	if !present {
		return "", true, nil
	}
	switch setting {
	case "true":
		return "", true, nil
	case "false":
		return "", false, nil
	default:
		return "", false, invalidCredentials("MySQL tls must be true or false")
	}
}

func uniqueQueryValue(values url.Values, name string) (string, bool, error) {
	var foundKey string
	for key := range values {
		if !strings.EqualFold(key, name) {
			continue
		}
		if foundKey != "" {
			return "", false, invalidCredentials("connection URI repeats a TLS setting")
		}
		foundKey = key
	}
	if foundKey == "" {
		return "", false, nil
	}
	entries := values[foundKey]
	if len(entries) != 1 {
		return "", false, invalidCredentials("connection URI repeats a TLS setting")
	}
	return entries[0], true, nil
}

func isUnsupportedTLSOption(key string) bool {
	return isPostgresCertificateOption(key) || isMySQLTLSOption(key)
}

func isPostgresCertificateOption(key string) bool {
	switch strings.ToLower(key) {
	case "sslrootcert", "sslcert", "sslkey", "sslcrl", "sslcrldir", "sslpassword":
		return true
	default:
		return false
	}
}

func isMySQLTLSOption(key string) bool {
	switch strings.ToLower(key) {
	case "tlsca", "tlscertificatekeyfile", "tlsallowinvalidcertificates", "tlsallowinvalidhostnames", "tlsinsecure", "tlsservername":
		return true
	// MySQL URI parameters use hyphenated names. Reject these even alongside
	// tls=true/false, so an explicit verification requirement cannot be ignored.
	case "ssl-mode", "ssl-ca", "ssl-capath", "ssl-cert", "ssl-key", "ssl-crl", "ssl-crlpath", "ssl-cipher", "ssl-verify-server-cert", "tls-version", "tls-ciphersuites":
		return true
	default:
		return false
	}
}

func validDatabaseURIScheme(databaseType, scheme string) bool {
	switch databaseType {
	case "postgresql":
		return strings.EqualFold(scheme, "postgres") || strings.EqualFold(scheme, "postgresql")
	case "mysql":
		return strings.EqualFold(scheme, "mysql")
	default:
		return false
	}
}

func validHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}

func validName(value string, maximum int) bool {
	if len(value) < 1 || len(value) > maximum {
		return false
	}
	for _, ch := range value {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' || ch == '.' {
			continue
		}
		return false
	}
	return true
}

func databaseLimit(databaseType string) int {
	if databaseType == "mysql" {
		return 64
	}
	return 63
}

func usernameLimit(databaseType string) int {
	if databaseType == "mysql" {
		return 32
	}
	return 63
}

func validPassword(password string) bool {
	if password == "" || !utf8.ValidString(password) || strings.ContainsRune(password, '\x00') {
		return false
	}
	if password != strings.TrimSpace(password) || strings.ContainsRune(password, '"') {
		return false
	}
	return true
}

func invalidCredentials(message string) error {
	return &driver.Error{Kind: driver.InvalidCredentials, Message: message}
}
