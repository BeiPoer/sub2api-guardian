package store

import (
	"path/filepath"
	"testing"
)

func TestScheduledReportSourceSettingsDefaultAndRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "guardian.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	settings, exists, err := st.ScheduledReportSourceSettings()
	if err != nil || exists {
		t.Fatalf("默认源站配置不应落库: settings=%+v exists=%v err=%v", settings, exists, err)
	}
	if settings.Mode != ScheduledReportSourceGlobal || settings.SourceType != ScheduledReportSourceSub2API {
		t.Fatalf("默认源站配置异常: %+v", settings)
	}

	saved, err := st.SaveScheduledReportSourceSettings(ScheduledReportSourceSettings{
		Mode: ScheduledReportSourceCustom, SourceType: ScheduledReportSourceNewAPI,
		BaseURL: " https://new.example.com/ ", Credential: " secret-token ", NewAPIUserID: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.BaseURL != "https://new.example.com" || saved.Credential != "secret-token" {
		t.Fatalf("源站配置未规范化: %+v", saved)
	}
	loaded, exists, err := st.ScheduledReportSourceSettings()
	if err != nil || !exists || loaded != saved {
		t.Fatalf("源站配置读取异常: loaded=%+v saved=%+v exists=%v err=%v", loaded, saved, exists, err)
	}
}
