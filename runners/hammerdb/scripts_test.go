package main

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTclLiteral(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"", `""`},
		{"a b;c", `"a b;c"`},
		{`[puts $x] {a} \"`, `"\[puts \$x\] \{a\} \\\""`},
		{"a\n\r\t\x01\x7fb", `"a\u000a\u000d\u0009\u0001\u007fb"`},
		{"päss密碼", `"päss密碼"`},
	} {
		if got := tclLiteral(tc.value); got != tc.want {
			t.Errorf("literal mismatch: got %q, want %q", got, tc.want)
		}
	}
}

func TestConfigurationScriptSettings(t *testing.T) {
	for _, engine := range []string{"postgresql", "mysql"} {
		for _, prepare := range []bool{true, false} {
			t.Run(engine+"/prepare="+strconv.FormatBool(prepare), func(t *testing.T) {
				env := validEnvironment(engine)
				env["BENCH_WAREHOUSES"], env["BENCH_VIRTUAL_USERS"] = "4", "2"
				env["BENCH_RAMPUP_MINUTES"], env["BENCH_DURATION_MINUTES"] = "2", "3"
				c, err := loadConfig(func(key string) string { return env[key] })
				if err != nil {
					t.Fatal(err)
				}
				script, err := configurationScript(c, prepare)
				if err != nil {
					t.Fatal(err)
				}
				prefix := "pg"
				if engine == "mysql" {
					prefix = "mysql"
				}
				expected := []string{
					`"dbset" "db" "` + prefix + `"`, `"dbset" "bm" "TPC-C"`,
					`"diset" "tpcc" "` + prefix + `_dbase" "benchmark"`,
					`"diset" "tpcc" "` + prefix + `_user" "inst-h7w"`,
					`"diset" "tpcc" "` + prefix + `_count_ware" "4"`,
					`"diset" "tpcc" "` + prefix + `_num_vu" "1"`,
				}
				if engine == "postgresql" {
					expected = append(expected,
						`"diset" "tpcc" "pg_superuser" "inst-h7w"`,
						`"diset" "tpcc" "pg_superuserpass" "dummy-secret"`,
						`"diset" "tpcc" "pg_defaultdbase" "benchmark"`,
						`"diset" "tpcc" "pg_vacuum" "false"`,
						`"diset" "connection" "pg_sslmode" "prefer"`)
				} else {
					expected = append(expected,
						`"diset" "connection" "mysql_ssl" "true"`,
						`"diset" "connection" "mysql_ssl_two_way" "false"`)
					for _, key := range []string{"socket", "ssl_linux_capath", "ssl_windows_capath", "ssl_ca", "ssl_cert", "ssl_key"} {
						expected = append(expected, `"diset" "connection" "mysql_`+key+`" ""`)
					}
				}
				if !prepare {
					for _, entry := range []string{`driver" "timed`, `rampup" "2`, `duration" "3`, `raiseerror" "true`, `timeprofile" "true`, `keyandthink" "false`, `allwarehouse" "false`, `async_scale" "false`, `connect_pool" "false`} {
						expected = append(expected, `"diset" "tpcc" "`+prefix+`_`+entry+`"`)
					}
					expected = append(expected, `"vuset" "vu" "2"`)
				} else if strings.Contains(script, `"vuset"`) {
					t.Fatal("preparation inherited measured virtual-user count")
				}
				for _, line := range expected {
					if !strings.Contains(script, line+"\n") {
						t.Errorf("missing setting: %s", line)
					}
				}
			})
		}
	}
}

func TestConfigurationScriptRejectsUnencodableValues(t *testing.T) {
	for _, field := range []string{"host", "database", "user", "password", "pgSSLMode"} {
		for _, value := range []string{"a\x00b", string([]byte{0xff})} {
			env := validEnvironment("postgresql")
			c, err := loadConfig(func(key string) string { return env[key] })
			if err != nil {
				t.Fatal(err)
			}
			fields := map[string]*string{"host": &c.host, "database": &c.database, "user": &c.user, "password": &c.password, "pgSSLMode": &c.pgSSLMode}
			*fields[field] = value
			if script, err := configurationScript(c, false); err == nil || script != "" {
				t.Fatal("unencodable setting produced a partial script")
			}
		}
	}
	if script, err := configurationScript(config{databaseType: "mongodb"}, false); err == nil || script != "" {
		t.Fatal("unsupported engine produced a script")
	}
}

// Set HAMMERDB_TEST_TCLSH to require a particular interpreter. Image CI must
// exercise the generated scripts with the pinned HammerDB CLI as well.
func TestConfigurationScriptTclRoundTrip(t *testing.T) {
	interpreter := os.Getenv("HAMMERDB_TEST_TCLSH")
	if interpreter == "" {
		var err error
		interpreter, err = exec.LookPath("tclsh")
		if err != nil {
			t.Skip("Tcl interpreter unavailable; set HAMMERDB_TEST_TCLSH to run the round-trip check")
		}
	}
	for _, engine := range []string{"postgresql", "mysql"} {
		for _, prepare := range []bool{true, false} {
			for _, password := range []string{`x]; set ::injected 1; #`, `x\;$HOME{[set ::injected 1]}\\`, "päss密碼\r\nlast\\"} {
				env := validEnvironment(engine)
				env["DB_PASSWORD"] = password
				c, err := loadConfig(func(key string) string { return env[key] })
				if err != nil {
					t.Fatal(err)
				}
				script, err := configurationScript(c, prepare)
				if err != nil {
					t.Fatal(err)
				}
				// Real Tcl evaluates the generated commands. Only HammerDB commands
				// are stubbed; captured credentials must survive exactly as one word.
				script = `set ::injected 0
proc dbset {args} {}
proc vuset {args} {}
proc diset {section key value} {
    if {[string match *_pass $key] || $key eq "pg_superuserpass"} {
        puts [binary encode base64 -maxlen 0 [encoding convertto utf-8 $value]]
    }
}
` + script + `if {$::injected != 0} {error "Tcl injection executed"}`
				path := filepath.Join(t.TempDir(), "test.tcl")
				if err := os.WriteFile(path, []byte(script), 0600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				output, err := exec.CommandContext(ctx, interpreter, path).CombinedOutput()
				cancel()
				if err != nil {
					t.Fatalf("Tcl evaluation failed: %v", err)
				}
				lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
				wantCount := 1
				if engine == "postgresql" {
					wantCount = 2
				}
				if len(lines) != wantCount {
					t.Fatal("unexpected Tcl output or missing credentials")
				}
				for _, line := range lines {
					decoded, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(line, "\r"))
					if err != nil || string(decoded) != password {
						t.Fatal("password did not round-trip through Tcl unchanged")
					}
				}
			}
		}
	}
}
