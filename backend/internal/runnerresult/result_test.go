package runnerresult

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeSharedFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "runner-result-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(string(data))
	want := []Metric{
		{Name: "tps", Unit: "tx/s", Value: 1234.5},
		{Name: "latency_avg", Unit: "ms", Value: 3.2},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Decode(fixture) = %+v, %v; want %+v", got, err, want)
	}
}

func TestDecodeAcceptsGenericMetrics(t *testing.T) {
	message := `{"version":1,"metrics":[
		{"name":"balance_delta","unit":"widgets","value":-2.5},
		{"name":"balance_delta","unit":"percent","value":0},
		{"name":" custom metric ","unit":" units/s ","value":1e3}
	]}`
	want := []Metric{
		{Name: "balance_delta", Unit: "widgets", Value: -2.5},
		{Name: "balance_delta", Unit: "percent", Value: 0},
		{Name: " custom metric ", Unit: " units/s ", Value: 1000},
	}
	got, err := Decode(message)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Decode() = %+v, %v; want %+v", got, err, want)
	}
	encoded, err := json.Marshal(got)
	if err != nil || !strings.Contains(string(encoded), `"value":0`) {
		t.Fatalf("zero metric was lost from JSON: %s, %v", encoded, err)
	}
}

func TestDecodeRejectsInvalidRecords(t *testing.T) {
	metricRecord := func(metric string) string {
		return `{"version":1,"metrics":[` + metric + `]}`
	}
	for _, test := range []struct{ name, message string }{
		{"empty", ""},
		{"whitespace", " \n "},
		{"invalid JSON", "{"},
		{"null record", "null"},
		{"array record", "[]"},
		{"string record", `"credential-sentinel"`},
		{"boolean record", "true"},
		{"missing version", `{"metrics":[{"name":"x","unit":"ms","value":1}]}`},
		{"null version", `{"version":null,"metrics":[{"name":"x","unit":"ms","value":1}]}`},
		{"wrong version", `{"version":2,"metrics":[{"name":"x","unit":"ms","value":1}]}`},
		{"wrong version type", `{"version":"1","metrics":[{"name":"x","unit":"ms","value":1}]}`},
		{"missing metrics", `{"version":1}`},
		{"null metrics", `{"version":1,"metrics":null}`},
		{"empty metrics", `{"version":1,"metrics":[]}`},
		{"object metrics", `{"version":1,"metrics":{}}`},
		{"null metric", metricRecord(`null`)},
		{"missing name", metricRecord(`{"unit":"ms","value":1}`)},
		{"null name", metricRecord(`{"name":null,"unit":"ms","value":1}`)},
		{"blank name", metricRecord(`{"name":" \t","unit":"ms","value":1}`)},
		{"wrong name type", metricRecord(`{"name":42,"unit":"ms","value":1}`)},
		{"missing unit", metricRecord(`{"name":"x","value":1}`)},
		{"null unit", metricRecord(`{"name":"x","unit":null,"value":1}`)},
		{"blank unit", metricRecord(`{"name":"x","unit":"\n ","value":1}`)},
		{"missing value", metricRecord(`{"name":"x","unit":"ms"}`)},
		{"null value", metricRecord(`{"name":"credential-sentinel","unit":"ms","value":null}`)},
		{"string value", metricRecord(`{"name":"x","unit":"ms","value":"credential-sentinel"}`)},
		{"boolean value", metricRecord(`{"name":"x","unit":"ms","value":false}`)},
		{"array value", metricRecord(`{"name":"x","unit":"ms","value":[]}`)},
		{"overflow", metricRecord(`{"name":"x","unit":"ms","value":1e999}`)},
		{"NaN", metricRecord(`{"name":"x","unit":"ms","value":NaN}`)},
		{"infinity", metricRecord(`{"name":"x","unit":"ms","value":Infinity}`)},
		{"unknown envelope field", `{"version":1,"credential-sentinel":1,"metrics":[{"name":"x","unit":"ms","value":1}]}`},
		{"unknown metric field", metricRecord(`{"name":"x","unit":"ms","value":1,"credential-sentinel":true}`)},
		{"duplicate metric", metricRecord(`{"name":"x","unit":"ms","value":1},{"name":"x","unit":"ms","value":2}`)},
		{"invalid second metric", metricRecord(`{"name":"x","unit":"ms","value":1},{"name":"y","unit":"ms"}`)},
		{"trailing object", metricRecord(`{"name":"x","unit":"ms","value":1}`) + " {}"},
		{"trailing null", metricRecord(`{"name":"x","unit":"ms","value":1}`) + " null"},
		{"trailing text", metricRecord(`{"name":"x","unit":"ms","value":1}`) + " credential-sentinel"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := Decode(test.message)
			if err == nil || got != nil {
				t.Fatal("invalid record returned metrics or no error")
			}
			if strings.Contains(err.Error(), "credential-sentinel") {
				t.Fatal("decoder error exposed submitted data")
			}
		})
	}
}

func TestDecodeSizeLimit(t *testing.T) {
	const base = `{"version":1,"metrics":[{"name":"x","unit":"ms","value":0}]}`
	name := strings.Repeat("x", maxRecordBytes-len(base)+1)
	message := strings.Replace(base, `"x"`, `"`+name+`"`, 1)
	if len(message) != maxRecordBytes {
		t.Fatal("test record is not exactly at the size limit")
	}
	got, err := Decode(message)
	if err != nil || len(got) != 1 || got[0].Name != name {
		t.Fatalf("record at size limit was rejected: %v", err)
	}
	for _, oversized := range []string{
		message + "\n",
		strings.Replace(base, `"x"`, `"`+strings.Repeat("é", maxRecordBytes/2)+`"`, 1),
	} {
		if got, err := Decode(oversized); err == nil || got != nil {
			t.Fatal("oversized record was accepted; size must be measured in bytes")
		}
	}
}
