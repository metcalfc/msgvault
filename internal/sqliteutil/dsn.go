package sqliteutil

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var serverDSNKeyword = regexp.MustCompile(`(?i)(?:^|\s)(?:` +
	`host|hostaddr|port|user|password|passfile|dbname|service|servicefile|` +
	`connect_timeout|application_name|fallback_application_name|options|` +
	`sslmode|sslcert|sslkey|sslpassword|sslrootcert|sslcrl|sslcrldir|sslsni|` +
	`ssl_min_protocol_version|ssl_max_protocol_version|target_session_attrs|` +
	`gssencmode|krbsrvname|gsslib|channel_binding|requirepeer|replication|` +
	`search_path|statement_timeout)\s*=`)

// ValidateDSN rejects server connection strings before any filesystem access.
// Do not include the supplied DSN in errors: it can contain credentials.
func ValidateDSN(dsn string) error {
	value := strings.ToLower(strings.TrimSpace(dsn))
	if strings.HasPrefix(value, "postgres:") || strings.HasPrefix(value, "postgresql:") ||
		(strings.Contains(value, "://") && !strings.HasPrefix(value, "file:")) {
		return errors.New("only SQLite database files are supported; PostgreSQL and other server database URLs are not supported")
	}
	if !strings.HasPrefix(value, "file:") && serverDSNKeyword.MatchString(value) {
		return errors.New("PostgreSQL connection strings are not supported; configure a SQLite database file")
	}
	return nil
}

// ResolveDSN returns the SQLite DSN to open and its backing filesystem path.
// It accepts plain paths, canonical file: URIs, and the file://C:%5C... form
// produced when net/url renders a raw Windows path as URL.Path.
func ResolveDSN(dsn string) (normalizedDSN, filesystemPath string, err error) {
	if err := ValidateDSN(dsn); err != nil {
		return "", "", err
	}
	if !strings.HasPrefix(dsn, "file:") {
		return dsn, strings.SplitN(dsn, "?", 2)[0], nil
	}

	if encodedPath, rawQuery, ok := windowsDriveFileURI(dsn); ok {
		path, decodeErr := url.PathUnescape(encodedPath)
		if decodeErr != nil {
			return "", "", fmt.Errorf("decode Windows file URI %q: %w", dsn, decodeErr)
		}
		uriPath := strings.ReplaceAll(path, `\`, "/")
		if !strings.HasPrefix(uriPath, "/") {
			uriPath = "/" + uriPath
		}
		normalized := (&url.URL{
			Scheme:   "file",
			Path:     uriPath,
			RawQuery: rawQuery,
		}).String()
		return normalized, path, nil
	}

	u, parseErr := url.Parse(dsn)
	if parseErr != nil {
		return "", "", fmt.Errorf("parse file URI %q: %w", dsn, parseErr)
	}
	path := u.Path
	if path == "" {
		path, parseErr = url.PathUnescape(u.Opaque)
		if parseErr != nil {
			return "", "", fmt.Errorf("decode file URI %q: %w", dsn, parseErr)
		}
	}
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		path = "//" + u.Host + path
	}
	if path == "" {
		return "", "", fmt.Errorf("empty file URI %q", dsn)
	}

	path = filepath.FromSlash(path)
	if runtime.GOOS == "windows" && len(path) >= 3 && os.IsPathSeparator(path[0]) && path[2] == ':' {
		path = path[1:]
	}
	return dsn, path, nil
}

func windowsDriveFileURI(dsn string) (encodedPath, rawQuery string, ok bool) {
	if !strings.HasPrefix(dsn, "file://") {
		return "", "", false
	}
	rest := strings.TrimPrefix(dsn, "file://")
	encodedPath, rawQuery, _ = strings.Cut(rest, "?")
	if len(encodedPath) < 2 || encodedPath[1] != ':' {
		return "", "", false
	}
	drive := encodedPath[0]
	if (drive < 'a' || drive > 'z') && (drive < 'A' || drive > 'Z') {
		return "", "", false
	}
	return encodedPath, rawQuery, true
}
