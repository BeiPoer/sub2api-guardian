package reports

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

type reportSource interface {
	Ready() error
	BaseURL() string
	ListUsage(context.Context, time.Time, time.Time, string) ([]upstream.UsageRecord, error)
	GetDailyReportStats(context.Context, time.Time, time.Time, string) (upstream.DailyReportStats, error)
}

func (m *Manager) SourceSettings() (SourceConfig, error) {
	settings, _, err := m.store.ScheduledReportSourceSettings()
	if err != nil {
		return SourceConfig{}, err
	}
	return m.sourceConfig(settings), nil
}

func (m *Manager) SaveSourceSettings(input SourceSaveInput) (SourceConfig, error) {
	current, _, err := m.store.ScheduledReportSourceSettings()
	if err != nil {
		return SourceConfig{}, err
	}
	if input.Mode != store.ScheduledReportSourceGlobal && input.Mode != store.ScheduledReportSourceCustom {
		return SourceConfig{}, invalid("源站模式必须是 global 或 custom")
	}
	if input.Mode == store.ScheduledReportSourceGlobal {
		current.Mode = store.ScheduledReportSourceGlobal
		saved, err := m.store.SaveScheduledReportSourceSettings(current)
		if err != nil {
			return SourceConfig{}, err
		}
		return m.sourceConfig(saved), nil
	}

	if input.SourceType != store.ScheduledReportSourceSub2API && input.SourceType != store.ScheduledReportSourceNewAPI {
		return SourceConfig{}, invalid("自定义源站类型必须是 sub2api 或 newapi")
	}
	baseURL, err := normalizeSourceURL(input.BaseURL)
	if err != nil {
		return SourceConfig{}, err
	}
	if input.SourceType == store.ScheduledReportSourceNewAPI && input.NewAPIUserID <= 0 {
		return SourceConfig{}, invalid("New API 用户 ID 必须是正整数")
	}
	credential := strings.TrimSpace(input.Credential)
	if credential == "" && current.SourceType == input.SourceType {
		credential = current.Credential
	}
	if credential == "" {
		if current.SourceType != input.SourceType {
			return SourceConfig{}, invalid("切换源站类型时必须填写对应凭据")
		}
		return SourceConfig{}, invalid("源站凭据不能为空")
	}

	settings := store.ScheduledReportSourceSettings{
		Mode:         store.ScheduledReportSourceCustom,
		SourceType:   input.SourceType,
		BaseURL:      baseURL,
		Credential:   credential,
		NewAPIUserID: input.NewAPIUserID,
	}
	if input.SourceType == store.ScheduledReportSourceSub2API {
		settings.NewAPIUserID = 0
	}
	saved, err := m.store.SaveScheduledReportSourceSettings(settings)
	if err != nil {
		return SourceConfig{}, err
	}
	return m.sourceConfig(saved), nil
}

func normalizeSourceURL(raw string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", invalid("源站地址必须是有效的 HTTP(S) 绝对地址")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", invalid("源站地址不能包含查询参数或片段")
	}
	return value, nil
}

func (m *Manager) sourceConfig(settings store.ScheduledReportSourceSettings) SourceConfig {
	config := SourceConfig{
		Mode:          settings.Mode,
		SourceType:    settings.SourceType,
		BaseURL:       settings.BaseURL,
		NewAPIUserID:  settings.NewAPIUserID,
		HasCredential: settings.Credential != "",
	}
	if settings.Mode == store.ScheduledReportSourceGlobal {
		config.Configured = m.client.Ready() == nil
		config.EffectiveType = store.ScheduledReportSourceSub2API
		config.EffectiveBaseURL = m.client.BaseURL()
		return config
	}
	config.Configured = customSourceConfigured(settings)
	config.EffectiveType = settings.SourceType
	config.EffectiveBaseURL = settings.BaseURL
	return config
}

func customSourceConfigured(settings store.ScheduledReportSourceSettings) bool {
	if settings.BaseURL == "" || settings.Credential == "" {
		return false
	}
	switch settings.SourceType {
	case store.ScheduledReportSourceSub2API:
		return true
	case store.ScheduledReportSourceNewAPI:
		return settings.NewAPIUserID > 0
	default:
		return false
	}
}

func (m *Manager) sourceSummary() (SourceSummary, error) {
	config, err := m.SourceSettings()
	if err != nil {
		return SourceSummary{}, err
	}
	return SourceSummary{
		Mode: config.Mode, Type: config.EffectiveType,
		Configured: config.Configured, BaseURL: config.EffectiveBaseURL,
	}, nil
}

func (m *Manager) resolveSource() (reportSource, error) {
	settings, _, err := m.store.ScheduledReportSourceSettings()
	if err != nil {
		return nil, err
	}
	if settings.Mode == store.ScheduledReportSourceGlobal {
		if err := m.client.Ready(); err != nil {
			return nil, err
		}
		return m.client, nil
	}
	if !customSourceConfigured(settings) {
		return nil, ErrSourceNotConfigured
	}
	var source reportSource
	switch settings.SourceType {
	case store.ScheduledReportSourceSub2API:
		source = upstream.New(settings.BaseURL, settings.Credential, time.Minute)
	case store.ScheduledReportSourceNewAPI:
		source = upstream.NewNewAPI(settings.BaseURL, settings.Credential, settings.NewAPIUserID, time.Minute)
	default:
		return nil, ErrSourceNotConfigured
	}
	if err := source.Ready(); err != nil {
		if errors.Is(err, upstream.ErrNotConfigured) {
			return nil, ErrSourceNotConfigured
		}
		return nil, err
	}
	return source, nil
}
