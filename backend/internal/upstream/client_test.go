package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestConcurrentReconfigureKeepsRequestSnapshotConsistent(t *testing.T) {
	server := func(wantKey string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("x-api-key"); got != wantKey {
				http.Error(w, fmt.Sprintf("key %q does not match server", got), http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": []map[string]any{{"id": 1, "name": "group"}},
			})
		}))
	}
	first := server("first-key")
	second := server("second-key")
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)

	client := New(first.URL, "first-key", 5*time.Second)
	var wg sync.WaitGroup
	errs := make(chan error, 200)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if i%2 == 0 {
				client.Reconfigure(first.URL, "first-key", 5*time.Second)
			} else {
				client.Reconfigure(second.URL, "second-key", 5*time.Second)
			}
		}
	}()
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if _, err := client.ListGroups(context.Background()); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发热更新产生了不一致请求: %v", err)
	}
}
