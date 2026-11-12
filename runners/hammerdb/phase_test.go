package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Only database/CLI operations are replaced. Tcl evaluates the real generated
// script, queries, preflight decisions, worker checks and completion-file logic.
const phaseTestCommands = `
package provide Pgtcl 2.1
package provide mysqltcl 3.0
set events {}
proc query_row {sql} {
    global mode
    if {$mode eq "query_error"} {error "private database error"}
    if {[string match "*current_schema()*" $sql]} {return 1}
    if {[string match "*pg_get_userbyid*" $sql]} {
        return [expr {$mode eq "wrong_owner" ? 0 : 1}]
    }
    if {[string match "*min(w_id)*" $sql]} {
        if {$mode eq "wrong_warehouses"} {return {2 1 2}}
        if {$mode eq "gapped_warehouses"} {return {1 2 2}}
        return {1 1 1}
    }
    if {[string match "*table_name IN*" $sql]} {
        return [expr {$mode eq "missing_tables" ? 8 : 9}]
    }
    return [expr {$mode eq "nonempty" ? 1 : 0}]
}
proc pg_connect {args} {
    if {$::mode eq "missing_database"} {error "private connection error"}
    return connection
}
proc pg_exec {connection sql} {set ::sql $sql; return result}
proc pg_result {result option args} {
    switch -- $option {
        -status {return PGRES_TUPLES_OK}
        -getTuple {return [query_row $::sql]}
        -clear {return}
    }
    error "unexpected Pgtcl option"
}
proc pg_disconnect {connection} {lappend ::events disconnect}
proc mysqlconnect {args} {return connection}
proc mysqluse {connection database} {
    if {$::mode eq "missing_database"} {error "private connection error"}
}
namespace eval mysql {
    proc sel {connection sql option} {
        if {$option ne "-list"} {error "unexpected MySQL option"}
        if {[string match "SHOW SESSION*" $sql]} {
            return [list [list Ssl_cipher [expr {$::mode eq "no_tls" ? "" : "TLS_AES_128_GCM_SHA256"}]]]
        }
        return [list [query_row $sql]]
    }
}
proc mysqlclose {connection} {lappend ::events disconnect}
proc dbset {args} {}
proc diset {args} {}
proc vuset {args} {}
proc loadscript {} {lappend ::events load}
proc vucreate {} {lappend ::events create}
proc populate_workers {count} {
    set ::jobid TEST123
    set ::vustatus {}
    for {set id 1} {$id <= $count} {incr id} {dict set ::vustatus $id {FINISH SUCCESS}}
    switch -- $::mode {
        failed_worker {dict set ::vustatus 1 {FINISH FAILED}}
        running_worker {dict set ::vustatus 1 RUNNING}
        missing_worker {dict unset ::vustatus 1}
        missing_job {unset ::jobid}
    }
}
proc buildschema {} {lappend ::events build; populate_workers 1}
proc vurun {} {
    lappend ::events run
    if {$::mode ne "no_work"} {populate_workers 2}
}
proc jobs {args} {
    if {$args eq "format JSON"} {return}
    if {$args ne "TEST123 timing"} {error "unexpected timing query"}
    puts {{"NEWORD":{"avg_ms":"9.61"}}}
}
proc vustatus {} {}
proc vudestroy {} {lappend ::events destroy; unset -nocomplain ::vustatus}
rename exit process_exit
proc exit {code} {puts "EVENTS $::events"; process_exit $code}
`

func TestPhaseScript(t *testing.T) {
	interpreter := os.Getenv("HAMMERDB_TEST_TCLSH")
	if interpreter == "" {
		var err error
		interpreter, err = exec.LookPath("tclsh")
		if err != nil {
			t.Skip("Tcl unavailable; set HAMMERDB_TEST_TCLSH to exercise phase scripts")
		}
	}
	for _, engine := range []string{"postgresql", "mysql"} {
		for _, prepare := range []bool{true, false} {
			modes := []string{"success", "missing_database", "query_error", "failed_worker", "running_worker", "missing_worker", "missing_job", "stale_completion"}
			if prepare {
				modes = append(modes, "nonempty")
				if engine == "postgresql" {
					modes = append(modes, "wrong_owner")
				}
			} else {
				modes = append(modes, "missing_tables", "wrong_warehouses", "gapped_warehouses", "no_work")
			}
			if engine == "mysql" {
				modes = append(modes, "no_tls")
			}
			for _, mode := range modes {
				t.Run(engine+"/"+map[bool]string{true: "prepare", false: "run"}[prepare]+"/"+mode, func(t *testing.T) {
					root := t.TempDir()
					// Hostile path characters exercise the literal encoder as well.
					completion := filepath.Join(root, "done [not-a-command] $value")
					if mode == "stale_completion" {
						if err := os.WriteFile(completion, []byte("stale"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					c, err := loadConfig(func(key string) string { return validEnvironment(engine)[key] })
					if err != nil {
						t.Fatal(err)
					}
					script, err := phaseScript(c, prepare, completion)
					if err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(root, "phase.tcl")
					if err := os.WriteFile(path, []byte("set mode "+tclLiteral(mode)+"\n"+phaseTestCommands+script), 0600); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					output, runErr := exec.CommandContext(ctx, interpreter, path).CombinedOutput()
					cancel()
					if mode == "success" {
						if runErr != nil {
							t.Fatalf("phase failed: %s", output)
						}
						data, err := os.ReadFile(completion)
						phase := "run"
						if prepare {
							phase = "prepare"
						}
						if err != nil || string(data) != "1 "+phase+" TEST123\n" {
							t.Fatal("phase did not record the verified job")
						}
						info, err := os.Stat(completion)
						if err != nil || info.Mode().Perm() != 0600 {
							t.Fatal("completion file is not private")
						}
						want := "EVENTS disconnect load create run destroy"
						if prepare {
							want = "EVENTS disconnect build destroy"
						}
						if !strings.Contains(string(output), want) {
							t.Fatalf("wrong phase order: %s", output)
						}
					} else {
						if runErr == nil || !strings.Contains(string(output), "HammerDB phase failed") {
							t.Fatalf("phase did not fail explicitly: %s", output)
						}
						if _, err := os.Stat(completion); !os.IsNotExist(err) {
							t.Fatal("failed phase left a completion record")
						}
						if mode == "missing_database" || mode == "query_error" || mode == "missing_tables" || mode == "wrong_warehouses" || mode == "gapped_warehouses" || mode == "nonempty" || mode == "wrong_owner" || mode == "no_tls" || mode == "stale_completion" {
							if strings.Contains(string(output), " load ") || strings.Contains(string(output), " build ") {
								t.Fatal("workload started after failed preflight")
							}
						}
					}
					if strings.Contains(string(output), c.password) || strings.Contains(string(output), "private connection error") || strings.Contains(string(output), "private database error") {
						t.Fatal("phase error exposed connection details")
					}
				})
			}
		}
	}
}
