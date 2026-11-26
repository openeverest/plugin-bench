package main

import (
	"slices"
	"strings"
	"testing"
)

func TestNewWorkloadArgs(t *testing.T) {
	c := config{keyCount: 750, valueSizeBytes: 128, threads: 4, clientsPerThread: 6, durationSeconds: 45}
	const resultPath = "/tmp/valkey test/results.json"
	got := newWorkloadArgs(c, resultPath)
	if !strings.HasPrefix(got.prefix, "plugin-bench:") || !strings.HasSuffix(got.prefix, ":") || got.prefix == "plugin-bench:" {
		t.Fatalf("unexpected benchmark key prefix %q", got.prefix)
	}
	if next := newWorkloadArgs(c, resultPath); next.prefix == got.prefix {
		t.Fatal("two runs share a benchmark key prefix")
	}

	common := []string{
		"--protocol=redis", "--select-db=0", "--key-prefix=" + got.prefix,
		"--key-minimum=1", "--key-maximum=750", "--data-size=128",
		"--expiry-range=3600-3601", "--hide-histogram",
	}
	wantSeed := append(slices.Clone(common),
		"--threads=1", "--clients=1", "--ratio=1:0", "--key-pattern=S:S", "--requests=750",
	)
	wantMeasure := append(slices.Clone(common),
		"--threads=4", "--clients=6", "--ratio=1:9", "--key-pattern=R:R", "--test-time=45",
		"--print-percentiles=95,99", "--json-out-file="+resultPath,
	)
	if !slices.Equal(got.seed, wantSeed) {
		t.Errorf("seed arguments = %q, want %q", got.seed, wantSeed)
	}
	if !slices.Equal(got.measure, wantMeasure) {
		t.Errorf("measurement arguments = %q, want %q", got.measure, wantMeasure)
	}
}
