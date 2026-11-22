package main

import (
	"crypto/rand"
	"strconv"
)

// The execution deadline must leave both phases enough time to finish before
// the earliest seeded keys expire. Each measured SET renews its key's TTL.
const keyExpiryRange = "3600-3601"

type workloadArgs struct {
	prefix  string
	seed    []string
	measure []string
}

// newWorkloadArgs allocates one key range for both Memtier invocations.
func newWorkloadArgs(c config, resultPath string) workloadArgs {
	prefix := "plugin-bench:" + rand.Text() + ":"
	common := []string{
		"--protocol=redis",
		"--select-db=0",
		"--key-prefix=" + prefix,
		"--key-minimum=1",
		"--key-maximum=" + strconv.Itoa(c.keyCount),
		"--data-size=" + strconv.Itoa(c.valueSizeBytes),
		"--expiry-range=" + keyExpiryRange,
		"--hide-histogram",
	}

	// Memtier counts requests per client. One sequential SET client
	// writes each key in the range once.
	seed := append(append([]string(nil), common...),
		"--threads=1",
		"--clients=1",
		"--ratio=1:0",
		"--key-pattern=S:S",
		"--requests="+strconv.Itoa(c.keyCount),
	)
	measure := append(common,
		"--threads="+strconv.Itoa(c.threads),
		"--clients="+strconv.Itoa(c.clientsPerThread),
		"--ratio=1:9",
		"--key-pattern=R:R",
		"--test-time="+strconv.Itoa(c.durationSeconds),
		"--print-percentiles=95,99",
		"--json-out-file="+resultPath,
	)
	return workloadArgs{prefix: prefix, seed: seed, measure: measure}
}
