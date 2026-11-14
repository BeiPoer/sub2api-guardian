package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestUpstreamYesCaptchaCounter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guardian.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	input := UpstreamChannelInput{Type: UpstreamChannelSub2API, BaseURL: "https://example.com", Username: "u", Password: "p"}
	a, err := st.CreateUpstreamChannel(input)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateUpstreamChannel(input)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟不含统计字段的旧数据库升级。
	if _, err := st.db.Exec(`ALTER TABLE upstream_channels DROP COLUMN yescaptcha_calls`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := st.UpstreamChannel(a.ID)
	if err != nil || loaded.YesCaptchaCalls != 0 {
		t.Fatalf("升级后应从零开始: %+v %v", loaded, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := st.IncrementUpstreamYesCaptchaCalls(a.ID); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := st.UpdateUpstreamChannel(a.ID, input); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]int64{a.ID: 10, b.ID: 0} {
		loaded, err := st.UpstreamChannel(id)
		if err != nil || loaded.YesCaptchaCalls != want {
			t.Fatalf("渠道 %d 统计不正确: got=%d want=%d err=%v", id, loaded.YesCaptchaCalls, want, err)
		}
	}
	if err := st.IncrementUpstreamYesCaptchaCalls(b.ID + 1); !errors.Is(err, ErrUpstreamChannelNotFound) {
		t.Fatalf("不存在的渠道: %v", err)
	}
}
