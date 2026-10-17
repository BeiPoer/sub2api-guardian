package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestChannelConfiguredModelsReadsSub2APIWithoutSync(t *testing.T) {
	fx := setupAuthAPI(t)
	var calls atomic.Int32
	var fail atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/admin/accounts/101/models" {
			t.Errorf("must only read configured models: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		writeEnvelope(w, []map[string]string{{"id": "gpt-5.6-sol"}, {"id": "claude-opus-4-6"}, {"id": "gpt-5.6-sol"}})
	}))
	defer upstream.Close()
	fx.server.client.Reconfigure(upstream.URL, "test-key", time.Second)
	cookie := &http.Cookie{Name: sessionCookie, Value: seedSession(t, fx.store, "admin", "hunter2hunter2")}
	res := raw(t, fx.handler, http.MethodGet, "/api/channels/101/models?source=sub2api", nil, cookie)
	var result struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if res.Code != http.StatusOK || calls.Load() != 1 || !reflect.DeepEqual(result.Models, []string{"claude-opus-4-6", "gpt-5.6-sol"}) {
		t.Fatalf("configured models: %d %s", res.Code, res.Body.String())
	}
	fail.Store(true)
	res = raw(t, fx.handler, http.MethodGet, "/api/channels/101/models?source=sub2api", nil, cookie)
	if res.Code == http.StatusOK || calls.Load() != 2 {
		t.Fatalf("failed read must not sync or become empty success: %d %s", res.Code, res.Body.String())
	}
}
