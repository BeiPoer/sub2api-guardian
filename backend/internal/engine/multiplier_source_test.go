package engine

import (
	"path/filepath"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

func TestResolveMultiplierSourceUsesCachedFinalRatio(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	channel, err := st.CreateUpstreamChannel(store.UpstreamChannelInput{
		Name: "source", Type: store.UpstreamChannelSub2API, BaseURL: "https://upstream.example.com",
		Username: "user", Password: "password", RechargeRatio: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkUpstreamChannelSynced(channel.ID); err != nil {
		t.Fatal(err)
	}
	groups := []any{map[string]any{"id": 1, "name": "pro", "user_rate_multiplier": 0.15}}
	tokens := []any{map[string]any{"key": "linked-key", "group_id": 1}}
	if err := st.SaveUpstreamCache(channel.ID, "groups", groups, groups); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveUpstreamCache(channel.ID, "tokens", tokens, tokens); err != nil {
		t.Fatal(err)
	}

	eng := New(st, upstream.New("", "", time.Second))
	fingerprint, ok := LinkedCredentialFingerprint("read-token", channel.BaseURL, "linked-key")
	if !ok {
		t.Fatal("生成指纹失败")
	}
	result, err := eng.ResolveMultiplierSource("read-token", []string{fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "ready" || !result.Complete || len(result.Items) != 1 || result.Items[0].Multiplier != 0.015 {
		t.Fatalf("倍率源结果异常: %+v", result)
	}
	if err := st.SaveUpstreamCache(channel.ID, "tokens", []any{
		map[string]any{"key": "linked-key********", "group_id": 1},
	}, []any{
		map[string]any{"key": "linked-key********", "group_id": 1},
	}); err != nil {
		t.Fatal(err)
	}
	partial, err := eng.ResolveMultiplierSource("read-token", []string{fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if partial.Complete || partial.State != "partial" || len(partial.Items) != 0 {
		t.Fatalf("脱敏 Key 不应形成完整快照: %+v", partial)
	}
}
