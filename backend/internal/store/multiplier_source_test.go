package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestMultiplierSourceSettingsPersistTokenWithoutExposingItOnModel(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	saved, err := st.SaveMultiplierSourceSettings(MultiplierSourceSettings{
		Mode: MultiplierSourceRemote, BaseURL: "https://g1.example.com/", Username: "admin",
		AccessToken: "secret-token", RemoteAccounts: map[string]RemoteLinkedAccount{
			"101": {Fingerprint: "fp", GeneratedName: "channel【x0.1】"},
		},
	})
	if err != nil || saved.AccessToken != "secret-token" {
		t.Fatalf("保存倍率源失败: %+v err=%v", saved, err)
	}
	loaded, exists, err := st.MultiplierSourceSettings()
	if err != nil || !exists || loaded.AccessToken != "secret-token" || loaded.BaseURL != "https://g1.example.com" {
		t.Fatalf("读取倍率源失败: %+v exists=%v err=%v", loaded, exists, err)
	}
	raw, _ := json.Marshal(loaded)
	if strings.Contains(string(raw), "secret-token") {
		t.Fatalf("公开配置模型泄露授权 Token: %s", raw)
	}
}
