package pgbench

import (
	"net/url"
	"strconv"
	"strings"

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
	if !validDatabaseName(database) {
		return resolvedConnection{}, invalidOptions("database must be a database name, not a connection string, and must not contain NUL")
	}
	if strings.TrimSpace(details.Password) == "" && strings.TrimSpace(details.URI) != "" {
		return resolvedConnection{}, &driver.Error{Kind: driver.UnsupportedConnection, Message: "URI-only credentials are not supported by the pgbench runner"}
	}
	for _, field := range []struct {
		name, value string
	}{
		{"host", details.Host},
		{"username", details.Username},
		{"password", details.Password},
	} {
		if strings.TrimSpace(field.value) == "" || strings.ContainsRune(field.value, '\x00') {
			return resolvedConnection{}, &driver.Error{Kind: driver.InvalidCredentials, Message: "database " + field.name + " is missing or invalid"}
		}
	}
	port, err := strconv.Atoi(strings.TrimSpace(details.Port))
	if err != nil || port < 1 || port > 65535 {
		return resolvedConnection{}, &driver.Error{Kind: driver.InvalidCredentials, Message: "database port must be an integer between 1 and 65535"}
	}
	if database == "" {
		database = strings.TrimSpace(details.Database)
		if database == "" && strings.TrimSpace(details.URI) != "" {
			database, err = databaseFromURI(details.URI)
			if err != nil {
				return resolvedConnection{}, err
			}
		}
		if !validDatabaseName(database) {
			return resolvedConnection{}, &driver.Error{Kind: driver.InvalidCredentials, Message: "default database is not a valid database name"}
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

// libpq treats a dbname containing '=' or a PostgreSQL URI as a connection
// string. Reject those forms even when they come from upstream database hints.
func validDatabaseName(database string) bool {
	lower := strings.ToLower(database)
	return !strings.ContainsAny(database, "=\x00") &&
		!strings.HasPrefix(lower, "postgres://") && !strings.HasPrefix(lower, "postgresql://")
}

func databaseFromURI(raw string) (string, error) {
	uri, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || uri.Host == "" || (!strings.EqualFold(uri.Scheme, "postgres") && !strings.EqualFold(uri.Scheme, "postgresql")) {
		return "", &driver.Error{Kind: driver.InvalidCredentials, Message: "connection URI does not contain a valid PostgreSQL database path"}
	}
	// url.Parse has already decoded escaped characters in Path. Nested paths,
	// including escaped slashes, cannot identify one database using this contract.
	database := strings.TrimPrefix(uri.Path, "/")
	if strings.Contains(database, "/") {
		return "", &driver.Error{Kind: driver.InvalidCredentials, Message: "connection URI must identify one database"}
	}
	return strings.TrimSpace(database), nil
}
