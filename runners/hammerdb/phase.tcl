# These procedures run inside HammerDB's interpreter. All runner_* values are
# supplied as encoded literals by phaseScript; database error text stays private.
proc runner_pg_literal {value} {
    return "'[string map [list \\ \\\\ ' \\'] $value]'"
}

proc runner_pg_query {connection sql} {
    set result [pg_exec $connection $sql]
    try {
        if {[pg_result $result -status] ne "PGRES_TUPLES_OK"} {
            error "database query failed"
        }
        return [pg_result $result -getTuple 0]
    } finally {
        pg_result $result -clear
    }
}

proc runner_preflight {} {
    global runner_engine runner_host runner_port runner_user runner_password
    global runner_database runner_pg_sslmode runner_mysql_ssl runner_prepare runner_warehouses
    set names "'warehouse','district','customer','history','new_order','orders','order_line','item','stock'"
    if {$runner_engine eq "postgresql"} {
        package require Pgtcl
        set fields {}
        foreach {key value} [list host $runner_host port $runner_port user $runner_user password $runner_password dbname $runner_database sslmode $runner_pg_sslmode connect_timeout 10] {
            lappend fields "$key=[runner_pg_literal $value]"
        }
        set connection [pg_connect -conninfo [join $fields " "]]
        try {
            # The builder and workers use the default search path. Reject a
            # different current schema instead of checking one and using another.
            if {[runner_pg_query $connection {SELECT CASE WHEN current_schema() = 'public' THEN 1 ELSE 0 END}] ne "1"} {
                error "unsupported PostgreSQL schema"
            }
            if {$runner_prepare} {
                if {[runner_pg_query $connection {SELECT CASE WHEN pg_get_userbyid(datdba) = current_user THEN 1 ELSE 0 END FROM pg_database WHERE datname = current_database()}] ne "1"} {
                    error "preparation requires the existing database owner"
                }
                # Installed extensions may own relations and functions in user
                # schemas. Only user-created objects make this database nonempty.
                set objects [runner_pg_query $connection {SELECT (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema' AND c.relkind IN ('r','p','v','m','S','f') AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')) + (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema' AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e'))}]
                if {$objects ne "0"} {error "preparation requires an empty database"}
            } else {
                set tables [runner_pg_query $connection "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE' AND table_name IN ($names)"]
                if {$tables ne "9"} {error "missing TPROC-C tables"}
                runner_check_warehouses [runner_pg_query $connection {SELECT count(*), min(w_id), max(w_id) FROM public.warehouse}]
            }
        } finally {
            pg_disconnect $connection
        }
    } else {
        package require mysqltcl
        # mysqltcl reads connect-timeout from the private HOME/.my.cnf staged
        # by writeScripts; its connection command has no -timeout option.
        # Use Tcl arguments directly; credentials must not be joined and eval'd.
        set connection [mysqlconnect -host $runner_host -port $runner_port -user $runner_user -password $runner_password -ssl $runner_mysql_ssl]
        try {
            mysqluse $connection $runner_database
            if {$runner_mysql_ssl} {
                set cipher [mysql::sel $connection {SHOW SESSION STATUS LIKE 'Ssl_cipher'} -list]
                if {[llength $cipher] != 1 || [lindex $cipher 0 1] eq ""} {error "TLS was not negotiated"}
            }
            if {$runner_prepare} {
                set objects [mysql::sel $connection {SELECT (SELECT count(*) FROM information_schema.tables WHERE table_schema = DATABASE()) + (SELECT count(*) FROM information_schema.routines WHERE routine_schema = DATABASE())} -list]
                if {[lindex $objects 0 0] ne "0"} {error "preparation requires an empty database"}
            } else {
                set tables [mysql::sel $connection "SELECT count(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE' AND table_name IN ($names)" -list]
                if {[lindex $tables 0 0] ne "9"} {error "missing TPROC-C tables"}
                runner_check_warehouses [lindex [mysql::sel $connection {SELECT count(*), min(w_id), max(w_id) FROM warehouse} -list] 0]
            }
        } finally {
            mysqlclose $connection
        }
    }
}

proc runner_check_warehouses {row} {
    global runner_warehouses
    if {[llength $row] != 3 || [lindex $row 0] ne $runner_warehouses || [lindex $row 1] ne "1" || [lindex $row 2] ne $runner_warehouses} {
        error "warehouse count or range does not match configuration"
    }
}

proc runner_check_workers {expected} {
    # Inspect before vudestroy removes the worker states. Both CLI phase
    # commands can return normally after a worker failed.
    global vustatus jobid
    if {![info exists jobid] || ![regexp {^[A-Za-z0-9]{1,128}$} $jobid]} {
        error "missing phase job ID"
    }
    vustatus
    if {![info exists vustatus] || [dict size $vustatus] != $expected} {
        error "incomplete worker statuses"
    }
    for {set id 1} {$id <= $expected} {incr id} {
        if {![dict exists $vustatus $id] || [dict get $vustatus $id] ne "FINISH SUCCESS"} {
            error "worker did not finish successfully"
        }
    }
    return $jobid
}

proc runner_complete {job} {
    global runner_completion runner_prepare
    set file [open $runner_completion {WRONLY CREAT EXCL} 0600]
    try {
        puts $file [list 1 [expr {$runner_prepare ? "prepare" : "run"}] $job]
    } finally {
        close $file
    }
}
