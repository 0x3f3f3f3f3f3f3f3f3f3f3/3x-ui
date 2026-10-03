package service

import (
	"errors"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

var errPostgresToolConnection = errors.New("invalid or unsupported PostgreSQL tool connection settings")

var postgresToolVariables = map[string]string{
	"host": "PGHOST", "port": "PGPORT", "dbname": "PGDATABASE", "user": "PGUSER", "password": "PGPASSWORD",
	"sslmode": "PGSSLMODE", "sslcert": "PGSSLCERT", "sslkey": "PGSSLKEY", "sslrootcert": "PGSSLROOTCERT",
	"sslcrl": "PGSSLCRL", "sslcrldir": "PGSSLCRLDIR", "sslsni": "PGSSLSNI",
	"ssl_min_protocol_version": "PGSSLMINPROTOCOLVERSION", "ssl_max_protocol_version": "PGSSLMAXPROTOCOLVERSION",
	"connect_timeout": "PGCONNECT_TIMEOUT", "target_session_attrs": "PGTARGETSESSIONATTRS",
	"application_name": "PGAPPNAME", "options": "PGOPTIONS", "passfile": "PGPASSFILE",
	"channel_binding": "PGCHANNELBINDING", "require_auth": "PGREQUIREAUTH",
}

// pgConnEnv keeps credentials out of tool arguments and resolves the same
// configured connection as pgx. Parse errors deliberately exclude the DSN.
func pgConnEnv(dsn string) ([]string, string, error) {
	settings, err := postgresToolSettings(strings.TrimSpace(dsn))
	if err != nil || settings["dbname"] == "" || settings["service"] != "" || settings["hostaddr"] != "" || os.Getenv("PGSERVICE") != "" {
		return nil, "", errPostgresToolConnection
	}
	cfg, err := pgconn.ParseConfig(strings.TrimSpace(dsn))
	if err != nil {
		return nil, "", errPostgresToolConnection
	}
	for _, fallback := range cfg.Fallbacks {
		if fallback.Host != cfg.Host || fallback.Port != cfg.Port {
			return nil, "", errPostgresToolConnection
		}
	}
	if strings.Contains(cfg.Host, ",") || strings.Contains(settings["host"], ",") || strings.Contains(settings["port"], ",") {
		return nil, "", errPostgresToolConnection
	}
	env := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	// libpq hostaddr can override PGHOST; pgx does not use this inherited hint.
	delete(env, "PGHOSTADDR")
	env["PGHOST"], env["PGPORT"], env["PGDATABASE"] = cfg.Host, strconv.Itoa(int(cfg.Port)), cfg.Database
	env["PGUSER"], env["PGPASSWORD"] = cfg.User, cfg.Password
	if env["PGSSLMODE"] == "" {
		env["PGSSLMODE"] = "prefer"
	}
	for key, value := range settings {
		if variable, ok := postgresToolVariables[key]; ok {
			env[variable] = value
		} else if _, runtime := cfg.RuntimeParams[key]; !runtime && key != "ssl" {
			return nil, "", errPostgresToolConnection
		}
	}
	var keys []string
	for key := range cfg.RuntimeParams {
		if _, mapped := postgresToolVariables[key]; !mapped {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	options := env["PGOPTIONS"]
	for _, key := range keys {
		if key == "" || strings.IndexAny(key, " \t\r\n\\=\x00") >= 0 {
			return nil, "", errPostgresToolConnection
		}
		value := cfg.RuntimeParams[key]
		if strings.IndexByte(value, 0) >= 0 {
			return nil, "", errPostgresToolConnection
		}
		escape := strings.NewReplacer("\\", "\\\\", " ", "\\ ", "\t", "\\\t", "\r", "\\\r", "\n", "\\\n")
		if options != "" {
			options += " "
		}
		options += "-c " + key + "=" + escape.Replace(value)
	}
	if options != "" {
		env["PGOPTIONS"] = options
	}
	keys = keys[:0]
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+env[key])
	}
	return result, cfg.Database, nil
}

func postgresToolSettings(dsn string) (map[string]string, error) {
	if strings.IndexByte(dsn, 0) >= 0 || dsn == "" {
		return nil, errPostgresToolConnection
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return postgresToolKeywordSettings(dsn)
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" || u.Fragment != "" || strings.Contains(u.Host, ",") {
		return nil, errPostgresToolConnection
	}
	settings := map[string]string{"dbname": strings.TrimPrefix(u.Path, "/")}
	if u.Hostname() != "" {
		settings["host"] = u.Hostname()
	}
	if u.Port() != "" {
		settings["port"] = u.Port()
	}
	if u.User != nil {
		settings["user"] = u.User.Username()
		if password, exists := u.User.Password(); exists {
			settings["password"] = password
		}
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errPostgresToolConnection
	}
	for key, values := range query {
		if key == "database" {
			key = "dbname"
		}
		settings[key] = values[len(values)-1]
	}
	if settings["ssl"] == "true" && settings["sslmode"] == "" {
		settings["sslmode"] = "require"
	}
	return settings, nil
}

// PostgreSQL keyword values use single quotes and backslash escaping, including
// unquoted escaped whitespace. This parser never includes input in its errors.
func postgresToolKeywordSettings(dsn string) (map[string]string, error) {
	space := func(b byte) bool { return strings.ContainsRune(" \t\n\r\v\f", rune(b)) }
	settings := make(map[string]string)
	for i := 0; i < len(dsn); {
		for i < len(dsn) && space(dsn[i]) {
			i++
		}
		if i == len(dsn) {
			break
		}
		start := i
		for i < len(dsn) && !space(dsn[i]) && dsn[i] != '=' {
			i++
		}
		key := dsn[start:i]
		for i < len(dsn) && space(dsn[i]) {
			i++
		}
		if key == "" || i == len(dsn) || dsn[i] != '=' {
			return nil, errPostgresToolConnection
		}
		i++
		for i < len(dsn) && space(dsn[i]) {
			i++
		}
		quoted := i < len(dsn) && dsn[i] == '\''
		if quoted {
			i++
		}
		closed := !quoted
		var value strings.Builder
		for i < len(dsn) {
			b := dsn[i]
			i++
			if b == '\\' {
				if i == len(dsn) {
					if quoted {
						return nil, errPostgresToolConnection
					}
					break
				}
				value.WriteByte(dsn[i])
				i++
			} else if quoted && b == '\'' {
				closed = true
				break
			} else if !quoted && space(b) {
				break
			} else {
				value.WriteByte(b)
			}
		}
		if !closed {
			return nil, errPostgresToolConnection
		}
		if key == "database" {
			key = "dbname"
		}
		settings[key] = value.String()
	}
	return settings, nil
}
