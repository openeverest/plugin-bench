package driver

// ErrorKind lets the API classify preparation failures without interpreting
// error strings or making the driver package depend on HTTP status codes.
type ErrorKind string

const (
	InvalidOptions        ErrorKind = "invalid_options"
	DriverSelection       ErrorKind = "driver_selection"
	UnsupportedConnection ErrorKind = "unsupported_connection"
	InvalidCredentials    ErrorKind = "invalid_credentials"
)

// Error describes a preparation failure. Message must be a safe explanation,
// never raw credentials, connection URIs, options JSON, or upstream error text.
// Raw causes are intentionally not retained to avoid leaking them when logged.
// Callers can use errors.As to inspect Kind when mapping the failure to HTTP.
type Error struct {
	Kind    ErrorKind
	Message string
}

func (e *Error) Error() string {
	var description string
	switch e.Kind {
	case InvalidOptions:
		description = "invalid benchmark options"
	case DriverSelection:
		description = "invalid driver selection"
	case UnsupportedConnection:
		description = "unsupported database connection"
	case InvalidCredentials:
		description = "invalid upstream database credentials"
	default:
		description = "driver preparation failed"
	}
	if e.Message != "" {
		return description + ": " + e.Message
	}
	return description
}
