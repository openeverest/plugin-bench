package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const modernRunBody = `{"target":{"k8sCluster":"local","namespace":"dbs","instance":"postgres-1"},"database":"bench","driver":"pgbench","options":{}}`

func TestDecodeCanonicalAndLegacyRequests(t *testing.T) {
	for _, test := range []struct{ name, body, wantOptions string }{
		{"modern omissions are driver defaults", modernRunBody, `{}`},
		{"legacy values are preserved", validCreateRunBody, `{"durationSeconds":30,"clients":1,"threads":1,"scale":1,"initialize":false}`},
		{"legacy omissions remain zero", `{"target":{"instance":"db"}}`, `{"durationSeconds":0,"clients":0,"threads":0,"scale":0,"initialize":false}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/json")
			request, err := decodeCreateRunRequest(httptest.NewRecorder(), r)
			if err != nil || request.Driver != "pgbench" || string(request.Options) != test.wantOptions {
				t.Fatalf("decoded driver=%q options=%s error=%v", request.Driver, request.Options, err)
			}
		})
	}
}

func TestDecodeRejectsMixedAndMalformedEnvelopes(t *testing.T) {
	for _, body := range []string{
		`{"driver":"pgbench"}`, `{"options":{}}`, `{"driver":null,"options":{}}`,
		`{"driver":"pgbench","options":null}`, `{"driver":"pgbench","options":[]}`,
		`{"driver":"pgbench","options":"text"}`, `{"driver":"pgbench","options":2}`,
		`{"driver":"pgbench","options":{},"clients":1}`, `{"driver":"pgbench","initialize":false}`,
		`{"driver":"pgbench","options":{},"image":"arbitrary:latest"}`,
		`{"driver":"pgbench","options":{},"resources":{"unexpected":"value"}}`,
		`{"driver":"pgbench","options":{},"target":{"provider":"untrusted"}}`,
		`{"driver":"pgbench","options":{}} {}`, `{"unexpected":true}`, `null`, `[]`,
	} {
		t.Run(body, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			if _, err := decodeCreateRunRequest(httptest.NewRecorder(), r); err == nil {
				t.Fatal("accepted malformed request")
			}
		})
	}
}

func TestDecodeLeavesDriverOptionsOpaque(t *testing.T) {
	// The HTTP layer accepts unfamiliar tool fields. Only the selected driver
	// can determine whether those fields are valid for its tool.
	body := `{"driver":"some-other-tool","options":{"arbitrary":[1,true]}}`
	r := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	request, err := decodeCreateRunRequest(httptest.NewRecorder(), r)
	if err != nil || !json.Valid(request.Options) || string(request.Options) != `{"arbitrary":[1,true]}` {
		t.Fatalf("options were interpreted: %s, %v", request.Options, err)
	}
}
