package store

import "strings"

const metaScheduledReportSource = "scheduled_report_source"

type ScheduledReportSourceMode string

const (
	ScheduledReportSourceGlobal ScheduledReportSourceMode = "global"
	ScheduledReportSourceCustom ScheduledReportSourceMode = "custom"
)

type ScheduledReportSourceType string

const (
	ScheduledReportSourceSub2API ScheduledReportSourceType = "sub2api"
	ScheduledReportSourceNewAPI  ScheduledReportSourceType = "newapi"
)

// ScheduledReportSourceSettings 是渠道使用报告和每日报告共用的源站配置。
type ScheduledReportSourceSettings struct {
	Mode         ScheduledReportSourceMode `json:"mode"`
	SourceType   ScheduledReportSourceType `json:"source_type"`
	BaseURL      string                    `json:"base_url"`
	Credential   string                    `json:"credential"`
	NewAPIUserID int64                     `json:"newapi_user_id"`
}

func DefaultScheduledReportSourceSettings() ScheduledReportSourceSettings {
	return ScheduledReportSourceSettings{
		Mode:       ScheduledReportSourceGlobal,
		SourceType: ScheduledReportSourceSub2API,
	}
}

func normalizeScheduledReportSourceSettings(settings *ScheduledReportSourceSettings) {
	if settings.Mode == "" {
		settings.Mode = ScheduledReportSourceGlobal
	}
	if settings.SourceType == "" {
		settings.SourceType = ScheduledReportSourceSub2API
	}
	settings.BaseURL = strings.TrimRight(strings.TrimSpace(settings.BaseURL), "/")
	settings.Credential = strings.TrimSpace(settings.Credential)
}

func (s *Store) ScheduledReportSourceSettings() (ScheduledReportSourceSettings, bool, error) {
	settings := DefaultScheduledReportSourceSettings()
	if err := s.getJSON(metaScheduledReportSource, &settings); err != nil {
		if IsNotFound(err) {
			return settings, false, nil
		}
		return ScheduledReportSourceSettings{}, false, err
	}
	normalizeScheduledReportSourceSettings(&settings)
	return settings, true, nil
}

func (s *Store) SaveScheduledReportSourceSettings(settings ScheduledReportSourceSettings) (ScheduledReportSourceSettings, error) {
	normalizeScheduledReportSourceSettings(&settings)
	s.mu.Lock()
	err := s.setJSON(metaScheduledReportSource, settings)
	s.mu.Unlock()
	if err != nil {
		return ScheduledReportSourceSettings{}, err
	}
	return settings, nil
}
