package store

import "strings"

const metaScheduledReportNotifications = "scheduled_report_notifications"

// ScheduledReportNotificationSettings 是所有定时报告共用的通知配置。
// 当前只有企微，后续通知渠道可以继续扩展这个配置而不绑定某一种报告。
type ScheduledReportNotificationSettings struct {
	WeCom ScheduledReportWeComSettings `json:"wecom"`
}

type ScheduledReportWeComSettings struct {
	Enabled   bool   `json:"enabled"`
	CorpID    string `json:"corp_id"`
	AgentID   int64  `json:"agent_id"`
	Secret    string `json:"secret"`
	Target    string `json:"target"`
	HasSecret bool   `json:"has_secret"`
}

type scheduledReportNotificationRecord struct {
	WeCom scheduledReportWeComRecord `json:"wecom"`
}

type scheduledReportWeComRecord struct {
	Enabled bool   `json:"enabled"`
	CorpID  string `json:"corp_id"`
	AgentID int64  `json:"agent_id"`
	Secret  string `json:"secret"`
	Target  string `json:"target"`
}

func DefaultScheduledReportNotificationSettings() ScheduledReportNotificationSettings {
	return ScheduledReportNotificationSettings{}
}

func normalizeScheduledReportNotificationSettings(settings *ScheduledReportNotificationSettings) {
	settings.WeCom.CorpID = strings.TrimSpace(settings.WeCom.CorpID)
	settings.WeCom.Secret = strings.TrimSpace(settings.WeCom.Secret)
	settings.WeCom.Target = strings.TrimSpace(settings.WeCom.Target)
	settings.WeCom.HasSecret = settings.WeCom.Secret != ""
}

func (s *Store) ScheduledReportNotificationSettings() (ScheduledReportNotificationSettings, bool, error) {
	settings := DefaultScheduledReportNotificationSettings()
	var record scheduledReportNotificationRecord
	if err := s.getJSON(metaScheduledReportNotifications, &record); err != nil {
		if IsNotFound(err) {
			return settings, false, nil
		}
		return ScheduledReportNotificationSettings{}, false, err
	}
	settings.WeCom = ScheduledReportWeComSettings{
		Enabled: record.WeCom.Enabled, CorpID: record.WeCom.CorpID,
		AgentID: record.WeCom.AgentID, Secret: record.WeCom.Secret, Target: record.WeCom.Target,
	}
	normalizeScheduledReportNotificationSettings(&settings)
	return settings, true, nil
}

func (s *Store) SaveScheduledReportNotificationSettings(settings ScheduledReportNotificationSettings) (ScheduledReportNotificationSettings, error) {
	normalizeScheduledReportNotificationSettings(&settings)
	record := scheduledReportNotificationRecord{
		WeCom: scheduledReportWeComRecord{
			Enabled: settings.WeCom.Enabled, CorpID: settings.WeCom.CorpID,
			AgentID: settings.WeCom.AgentID, Secret: settings.WeCom.Secret, Target: settings.WeCom.Target,
		},
	}
	s.mu.Lock()
	err := s.setJSON(metaScheduledReportNotifications, record)
	s.mu.Unlock()
	if err != nil {
		return ScheduledReportNotificationSettings{}, err
	}
	return settings, nil
}
