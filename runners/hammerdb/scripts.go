package main

import (
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

//go:embed phase.tcl
var phaseSupport string

// phaseScript wraps configuration in a checked, noninteractive phase. Completion
// is a fresh private file, never inferred from the CLI's exit status alone.
func phaseScript(c config, prepare bool, completion string) (string, error) {
	settings, err := configurationScript(c, prepare)
	if err != nil {
		return "", err
	}
	if completion == "" || !utf8.ValidString(completion) || strings.ContainsRune(completion, '\x00') {
		return "", errors.New("invalid private HammerDB completion path")
	}
	var b strings.Builder
	b.WriteString(phaseSupport)
	for _, entry := range []struct{ key, value string }{
		{"engine", c.databaseType}, {"host", c.host}, {"port", strconv.Itoa(c.port)},
		{"user", c.user}, {"password", c.password}, {"database", c.database},
		{"pg_sslmode", c.pgSSLMode}, {"mysql_ssl", strconv.FormatBool(c.mysqlSSL)},
		{"prepare", strconv.FormatBool(prepare)}, {"warehouses", strconv.Itoa(c.warehouses)},
		{"completion", completion},
	} {
		fmt.Fprintf(&b, "set runner_%s %s\n", entry.key, tclLiteral(entry.value))
	}
	b.WriteString("try {\nif {[file exists $runner_completion]} {error \"completion file already exists\"}\nrunner_preflight\n")
	b.WriteString(settings)
	// A new process is expected for each phase. Clear stale status explicitly
	// rather than accepting leftovers if a CLI command returns without working.
	b.WriteString("unset -nocomplain ::jobid ::vustatus\n")
	if prepare {
		b.WriteString("buildschema\nset runner_job [runner_check_workers 1]\n")
	} else {
		fmt.Fprintf(&b, "loadscript\nvucreate\nvurun\nset runner_job [runner_check_workers %d]\n", int64(c.virtualUsers)+1)
	}
	b.WriteString(`vudestroy
runner_complete $runner_job
} on error {message options} {
    catch {vudestroy}
    catch {file delete -force $runner_completion}
    puts stderr "HammerDB phase failed: check database access, schema and worker status"
    exit 1
}
exit 0
`)
	return b.String(), nil
}

// tclLiteral encodes one word inside double quotes. In particular, Tcl must
// not evaluate command substitutions, variables, or backslash sequences in it.
func tclLiteral(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, ch := range value {
		switch ch {
		case '\\', '"', '$', '[', ']', '{', '}':
			b.WriteByte('\\')
			b.WriteRune(ch)
		default:
			if ch < 32 || ch == 127 {
				fmt.Fprintf(&b, `\u%04x`, ch)
			} else {
				b.WriteRune(ch)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// configurationScript consumes a config from loadConfig. It selects the engine
// and phase settings, but does not start workers. The execution layer must add
// database/schema checks and completion handling before buildschema or vurun.
// The returned text contains credentials and belongs only in a private file.
func configurationScript(c config, prepare bool) (string, error) {
	if c.databaseType != "postgresql" && c.databaseType != "mysql" {
		return "", errors.New("unsupported HammerDB database type")
	}
	if err := validatePassword(c.password); err != nil {
		return "", err
	}
	for _, value := range []string{c.host, c.database, c.user, c.password, c.pgSSLMode} {
		if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return "", errors.New("HammerDB settings must be valid UTF-8 without NUL bytes")
		}
	}
	var b strings.Builder
	command := func(words ...string) {
		for i, word := range words {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(tclLiteral(word))
		}
		b.WriteByte('\n')
	}
	setting := func(section, key, value string) { command("diset", section, key, value) }
	prefix := "pg"
	if c.databaseType == "mysql" {
		prefix = "mysql"
	}
	command("dbset", "db", prefix)
	command("dbset", "bm", "TPC-C")
	setting("connection", prefix+"_host", c.host)
	setting("connection", prefix+"_port", strconv.Itoa(c.port))
	if prefix == "pg" {
		setting("connection", "pg_sslmode", c.pgSSLMode)
		setting("tpcc", "pg_superuser", c.user)
		setting("tpcc", "pg_superuserpass", c.password)
		setting("tpcc", "pg_defaultdbase", c.database)
		setting("tpcc", "pg_vacuum", "false")
	} else {
		setting("connection", "mysql_socket", "")
		setting("connection", "mysql_ssl", strconv.FormatBool(c.mysqlSSL))
		setting("connection", "mysql_ssl_two_way", "false")
		for _, key := range []string{"mysql_ssl_linux_capath", "mysql_ssl_windows_capath", "mysql_ssl_ca", "mysql_ssl_cert", "mysql_ssl_key"} {
			setting("connection", key, "")
		}
		setting("tpcc", "mysql_storage_engine", "innodb")
	}
	setting("tpcc", prefix+"_user", c.user)
	setting("tpcc", prefix+"_pass", c.password)
	setting("tpcc", prefix+"_dbase", c.database)
	setting("tpcc", prefix+"_count_ware", strconv.Itoa(c.warehouses))
	// Schema loading uses one worker independently of measured concurrency.
	setting("tpcc", prefix+"_num_vu", "1")
	if !prepare {
		for _, entry := range []struct{ key, value string }{
			{"driver", "timed"}, {"rampup", strconv.Itoa(c.rampupMinutes)},
			{"duration", strconv.Itoa(c.durationMinutes)}, {"raiseerror", "true"},
			{"timeprofile", "true"}, {"keyandthink", "false"},
			{"allwarehouse", "false"}, {"async_scale", "false"}, {"connect_pool", "false"},
		} {
			setting("tpcc", prefix+"_"+entry.key, entry.value)
		}
		command("vuset", "vu", strconv.Itoa(c.virtualUsers))
	}
	return b.String(), nil
}
