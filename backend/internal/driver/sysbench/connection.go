package sysbench

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

// resolvedConnection contains validated credentials for the runner. Never log it.
type resolvedConnection struct {
	Host, Database, Username, Password string
	Port                               int
	URI                                string
}

func resolveConnection(requestedDatabase string, details driver.ConnectionDetails) (resolvedConnection, error) {
	database := strings.TrimSpace(requestedDatabase)
	if strings.ContainsAny(requestedDatabase, "\x00\r\n") {
		return resolvedConnection{}, invalidOptions("database must not contain NUL bytes or line breaks")
	}
	if err := validateConnectionValue("mysql-db", database); err != nil {
		return resolvedConnection{}, invalidOptions("database " + err.Error())
	}
	if details.Password == "" && strings.TrimSpace(details.URI) != "" {
		return resolvedConnection{}, &driver.Error{Kind: driver.UnsupportedConnection, Message: "URI-only credentials are not supported by the sysbench runner"}
	}
	for _, field := range []struct {
		name, option, value string
	}{
		{"host", "mysql-host", details.Host},
		{"username", "mysql-user", details.Username},
		{"password", "mysql-password", details.Password},
	} {
		if field.value == "" || (field.name != "password" && strings.TrimSpace(field.value) == "") {
			return resolvedConnection{}, invalidCredentials("database " + field.name + " is missing or invalid")
		}
		if err := validateConnectionValue(field.option, field.value); err != nil {
			return resolvedConnection{}, invalidCredentials("database " + field.name + " " + err.Error())
		}
	}
	if strings.Contains(details.Host, ",") {
		return resolvedConnection{}, invalidCredentials("database host must specify one host")
	}
	port, err := strconv.Atoi(strings.TrimSpace(details.Port))
	if err != nil || port < 1 || port > 65535 || strings.ContainsAny(details.Port, "\x00\r\n") {
		return resolvedConnection{}, invalidCredentials("database port must be an integer between 1 and 65535")
	}

	if database == "" {
		if strings.ContainsAny(details.Database, "\x00\r\n") {
			return resolvedConnection{}, invalidCredentials("default database must not contain NUL bytes or line breaks")
		}
		database = strings.TrimSpace(details.Database)
		if database == "" && strings.TrimSpace(details.URI) != "" {
			database, err = databaseFromURI(details.URI)
			if err != nil {
				return resolvedConnection{}, err
			}
		}
		if err := validateConnectionValue("mysql-db", database); err != nil {
			return resolvedConnection{}, invalidCredentials("default database " + err.Error())
		}
	}
	if database == "" {
		return resolvedConnection{}, invalidOptions("could not determine the instance's default database; enter a database name")
	}
	return resolvedConnection{
		Host: details.Host, Port: port, Database: database,
		Username: details.Username, Password: details.Password, URI: details.URI,
	}, nil
}

func databaseFromURI(raw string) (string, error) {
	// Reject controls before trimming, including controls decoded in the path.
	if strings.ContainsAny(raw, "\x00\r\n") {
		return "", invalidCredentials("connection URI must not contain NUL bytes or line breaks")
	}
	uri, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || uri.Hostname() == "" || !strings.EqualFold(uri.Scheme, "mysql") {
		return "", invalidCredentials("connection URI does not contain a valid MySQL database path")
	}
	// net/url decodes escaped path bytes. A nested path, even an escaped slash,
	// cannot identify one database under this connection contract.
	database := strings.TrimPrefix(uri.Path, "/")
	if strings.Contains(database, "/") {
		return "", invalidCredentials("connection URI must identify one database")
	}
	if strings.ContainsAny(database, "\x00\r\n") {
		return "", invalidCredentials("default database must not contain NUL bytes or line breaks")
	}
	return strings.TrimSpace(database), nil
}

// validateConnectionValue mirrors the standalone runner's configuration format
// so unsupported credentials fail before admission. Keep these rules aligned
// with runners/sysbench/credentials.go; no file or escaped value is produced here.
func validateConnectionValue(option, value string) error {
	if strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("must not contain NUL bytes or line breaks")
	}
	quoteBytes := 2
	if strings.Contains(value, "'") && strings.Contains(value, `"`) {
		if strings.ContainsRune(value, ',') || strings.ContainsFunc(value, unicode.IsSpace) || strings.ContainsAny(value[:1], "'\"#") {
			return errors.New("cannot be represented in the sysbench configuration file")
		}
		quoteBytes = 0
	}
	// The limit includes the option name, '=', optional quotes, and newline.
	if len(option)+1+len(value)+quoteBytes+1 > 254 {
		return errors.New("exceeds the sysbench configuration line limit")
	}
	return nil
}

func invalidCredentials(message string) error {
	return &driver.Error{Kind: driver.InvalidCredentials, Message: message}
}
