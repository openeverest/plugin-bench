package valkey

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/openeverest/plugin-bench/backend/internal/driver"
)

const benchmarkDatabase = "0"

func supportsTarget(target driver.TargetInfo) bool {
	return strings.EqualFold(strings.TrimSpace(target.DatabaseType), "valkey") &&
		strings.EqualFold(strings.TrimSpace(target.Provider), "valkey")
}

// resolvedConnection contains execution credentials. Keep it out of logs and snapshots.
type resolvedConnection struct {
	Host, Port, Username, Password string
	TLS                            bool
	CACertificate                  string
	ClientCertificate, ClientKey   string
}

func resolveConnection(requestedDatabase string, details driver.ConnectionDetails) (resolvedConnection, error) {
	database := strings.TrimSpace(requestedDatabase)
	if database != "" && database != benchmarkDatabase {
		return resolvedConnection{}, invalidOptions("valkey supports only database 0")
	}
	if details.Database != "" && details.Database != benchmarkDatabase {
		return resolvedConnection{}, invalidCredentials("Valkey connection must use database 0")
	}
	if !validHost(details.Host) || !validPort(details.Port) {
		return resolvedConnection{}, invalidCredentials("Valkey connection requires a valid host and port")
	}
	if !validCredential(details.Username) || strings.ContainsRune(details.Username, ':') {
		return resolvedConnection{}, invalidCredentials("Valkey connection requires a valid ACL username without a colon")
	}
	if !validCredential(details.Password) {
		return resolvedConnection{}, invalidCredentials("Valkey connection requires a valid ACL password")
	}

	// The provider publishes structured credentials and uses the URI scheme for
	// TLS. Its URI userinfo uses query escaping, so do not decode it as a password.
	uri, err := url.Parse(details.URI)
	if err != nil || uri.Host == "" || uri.Fragment != "" || uri.RawQuery != "" || uri.ForceQuery {
		return resolvedConnection{}, invalidCredentials("Valkey connection requires a valid redis:// or rediss:// URI")
	}
	if !strings.EqualFold(uri.Hostname(), details.Host) || uri.Port() != details.Port {
		return resolvedConnection{}, invalidCredentials("Valkey connection URI disagrees with the published host or port")
	}
	if uri.Path != "" && uri.Path != "/" && uri.Path != "/0" {
		return resolvedConnection{}, invalidCredentials("Valkey connection URI must use database 0")
	}

	if details.SSL != nil && *details.SSL != strconv.FormatBool(uri.Scheme == "rediss") {
		return resolvedConnection{}, invalidCredentials("Valkey ssl setting must match the connection URI scheme")
	}

	switch uri.Scheme {
	case "redis":
		if details.CACertificate != "" || details.ClientCertificate != "" || details.ClientKey != "" {
			return resolvedConnection{}, invalidCredentials("Valkey TLS material requires a rediss:// connection")
		}
	case "rediss":
		if !x509.NewCertPool().AppendCertsFromPEM([]byte(details.CACertificate)) {
			return resolvedConnection{}, invalidCredentials("Valkey TLS requires a valid PEM CA certificate")
		}
		if (details.ClientCertificate == "") != (details.ClientKey == "") {
			return resolvedConnection{}, invalidCredentials("Valkey mTLS requires both a client certificate and a private key")
		}
		if details.ClientCertificate != "" {
			if _, err := tls.X509KeyPair([]byte(details.ClientCertificate), []byte(details.ClientKey)); err != nil {
				return resolvedConnection{}, invalidCredentials("Valkey mTLS requires a valid matching client certificate and private key")
			}
		}
	default:
		return resolvedConnection{}, invalidCredentials("Valkey connection requires a redis:// or rediss:// URI")
	}

	return resolvedConnection{
		Host: details.Host, Port: details.Port, Username: details.Username, Password: details.Password,
		TLS: uri.Scheme == "rediss", CACertificate: details.CACertificate,
		ClientCertificate: details.ClientCertificate, ClientKey: details.ClientKey,
	}, nil
}

func validPort(value string) bool {
	port, err := strconv.ParseUint(value, 10, 16)
	return err == nil && port != 0
}

func validCredential(value string) bool {
	return value != "" && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

// Match the runner's IP and DNS hostname rules before creating a Job.
func validHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	host = strings.TrimSuffix(host, ".")
	if len(host) > 253 {
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

func invalidCredentials(message string) error {
	return &driver.Error{Kind: driver.InvalidCredentials, Message: message}
}
