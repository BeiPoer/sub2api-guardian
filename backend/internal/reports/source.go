package reports

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

func (m *Manager) SourceSettings() (SourceCatalogConfig, error) {
	catalog, _, err := m.store.ScheduledReportSources()
	if err != nil {
		return SourceCatalogConfig{}, err
	}
	return m.sourceCatalogConfig(catalog), nil
}

func (m *Manager) SaveSourceSettings(input SourceSaveInput) (SourceCatalogConfig, error) {
	catalog, _, err := m.store.ScheduledReportSources()
	if err != nil {
		return SourceCatalogConfig{}, err
	}
	if input.Mode != "" && input.Mode != store.ScheduledReportSourceGlobal && input.Mode != store.ScheduledReportSourceCustom {
		return SourceCatalogConfig{}, invalid("源站模式必须是 global 或 custom")
	}
	// 兼容旧版客户端的“切换回全局”请求；新版报告直接保存各自的 source_id。
	if input.Mode == store.ScheduledReportSourceGlobal {
		catalog.DefaultSourceID = store.ScheduledReportGlobalSourceID
		saved, err := m.store.SaveScheduledReportSources(catalog)
		if err != nil {
			return SourceCatalogConfig{}, err
		}
		return m.sourceCatalogConfig(saved), nil
	}

	id := strings.TrimSpace(input.ID)
	if id == "" && input.Mode == store.ScheduledReportSourceCustom &&
		catalog.DefaultSourceID != store.ScheduledReportGlobalSourceID {
		id = catalog.DefaultSourceID
	}
	index := sourceIndex(catalog, id)
	if id != "" && index < 0 {
		return SourceCatalogConfig{}, &Error{Status: http.StatusNotFound, Message: "报告源站不存在"}
	}
	name := strings.TrimSpace(input.Name)
	if name == "" && input.Mode == store.ScheduledReportSourceCustom {
		name = "自定义源站"
		if index >= 0 {
			name = catalog.Sources[index].Name
		}
	}
	if name == "" {
		return SourceCatalogConfig{}, invalid("源站名称不能为空")
	}
	if len([]rune(name)) > 100 {
		return SourceCatalogConfig{}, invalid("源站名称不能超过 100 个字符")
	}
	if input.SourceType != store.ScheduledReportSourceSub2API && input.SourceType != store.ScheduledReportSourceNewAPI {
		return SourceCatalogConfig{}, invalid("自定义源站类型必须是 sub2api 或 newapi")
	}
	baseURL, err := normalizeSourceURL(input.BaseURL)
	if err != nil {
		return SourceCatalogConfig{}, err
	}
	if input.SourceType == store.ScheduledReportSourceNewAPI && input.NewAPIUserID <= 0 {
		return SourceCatalogConfig{}, invalid("New API 用户 ID 必须是正整数")
	}

	credential := strings.TrimSpace(input.Credential)
	if credential == "" && index >= 0 && catalog.Sources[index].SourceType == input.SourceType {
		credential = catalog.Sources[index].Credential
	}
	if credential == "" {
		if index >= 0 && catalog.Sources[index].SourceType != input.SourceType {
			return SourceCatalogConfig{}, invalid("切换源站类型时必须填写对应凭据")
		}
		return SourceCatalogConfig{}, invalid("源站凭据不能为空")
	}

	if id == "" {
		id = nextSourceID(&catalog)
	}
	source := store.ScheduledReportSource{
		ID: id, Name: name, SourceType: input.SourceType,
		BaseURL: baseURL, Credential: credential, NewAPIUserID: input.NewAPIUserID,
	}
	if input.SourceType == store.ScheduledReportSourceSub2API {
		source.NewAPIUserID = 0
	}
	if index >= 0 {
		catalog.Sources[index] = source
	} else {
		catalog.Sources = append(catalog.Sources, source)
	}
	if input.Mode == store.ScheduledReportSourceCustom {
		catalog.DefaultSourceID = id
	}
	saved, err := m.store.SaveScheduledReportSources(catalog)
	if err != nil {
		return SourceCatalogConfig{}, err
	}
	return m.sourceCatalogConfig(saved), nil
}

func (m *Manager) DeleteSourceSettings(id string) (SourceCatalogConfig, error) {
	id = strings.TrimSpace(id)
	if id == "" || id == store.ScheduledReportGlobalSourceID {
		return SourceCatalogConfig{}, invalid("全局源站不能删除")
	}
	catalog, _, err := m.store.ScheduledReportSources()
	if err != nil {
		return SourceCatalogConfig{}, err
	}
	index := sourceIndex(catalog, id)
	if index < 0 {
		return SourceCatalogConfig{}, &Error{Status: http.StatusNotFound, Message: "报告源站不存在"}
	}
	usedBy, err := m.sourceUsedBy(id, catalog)
	if err != nil {
		return SourceCatalogConfig{}, err
	}
	if len(usedBy) > 0 {
		return SourceCatalogConfig{}, &Error{
			Status:  http.StatusConflict,
			Message: fmt.Sprintf("源站正在被%s使用，请先修改报告配置", strings.Join(usedBy, "、")),
		}
	}
	catalog.Sources = append(catalog.Sources[:index], catalog.Sources[index+1:]...)
	if catalog.DefaultSourceID == id {
		catalog.DefaultSourceID = store.ScheduledReportGlobalSourceID
	}
	saved, err := m.store.SaveScheduledReportSources(catalog)
	if err != nil {
		return SourceCatalogConfig{}, err
	}
	return m.sourceCatalogConfig(saved), nil
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

func (m *Manager) sourceCatalogConfig(catalog store.ScheduledReportSourceCatalog) SourceCatalogConfig {
	items := []SourceConfig{m.globalSourceConfig()}
	for _, source := range catalog.Sources {
		items = append(items, customSourceConfig(source))
	}
	selected := items[0]
	for _, item := range items {
		if item.ID == effectiveSourceID("", catalog) {
			selected = item
			break
		}
	}
	return SourceCatalogConfig{SourceConfig: selected, Items: items}
}

func (m *Manager) globalSourceConfig() SourceConfig {
	baseURL := m.client.BaseURL()
	return SourceConfig{
		ID: store.ScheduledReportGlobalSourceID, Name: "全局 Sub2API",
		Mode: store.ScheduledReportSourceGlobal, SourceType: store.ScheduledReportSourceSub2API,
		Configured: m.client.Ready() == nil, EffectiveType: store.ScheduledReportSourceSub2API,
		EffectiveBaseURL: baseURL,
	}
}

func customSourceConfig(source store.ScheduledReportSource) SourceConfig {
	return SourceConfig{
		ID: source.ID, Name: source.Name, Mode: store.ScheduledReportSourceCustom,
		SourceType: source.SourceType, BaseURL: source.BaseURL, NewAPIUserID: source.NewAPIUserID,
		HasCredential: source.Credential != "", Configured: customSourceConfigured(source),
		EffectiveType: source.SourceType, EffectiveBaseURL: source.BaseURL,
	}
}

func customSourceConfigured(source store.ScheduledReportSource) bool {
	if source.BaseURL == "" || source.Credential == "" {
		return false
	}
	switch source.SourceType {
	case store.ScheduledReportSourceSub2API:
		return true
	case store.ScheduledReportSourceNewAPI:
		return source.NewAPIUserID > 0
	default:
		return false
	}
}

func (m *Manager) sourceView(sourceID string) (SourceSummary, []SourceSummary, error) {
	catalog, _, err := m.store.ScheduledReportSources()
	if err != nil {
		return SourceSummary{}, nil, err
	}
	sourceID = effectiveSourceID(sourceID, catalog)
	configs := m.sourceCatalogConfig(catalog).Items
	summaries := make([]SourceSummary, 0, len(configs))
	for _, config := range configs {
		summary := sourceSummaryOf(config)
		summaries = append(summaries, summary)
		if config.ID == sourceID {
			return summary, summariesWithRest(summaries, configs[len(summaries):]), nil
		}
	}
	return SourceSummary{
		ID: sourceID, Name: "已删除的源站", Mode: store.ScheduledReportSourceCustom,
		Configured: false,
	}, summaries, nil
}

func summariesWithRest(summaries []SourceSummary, configs []SourceConfig) []SourceSummary {
	for _, config := range configs {
		summaries = append(summaries, sourceSummaryOf(config))
	}
	return summaries
}

func sourceSummaryOf(config SourceConfig) SourceSummary {
	return SourceSummary{
		ID: config.ID, Name: config.Name, Mode: config.Mode, Type: config.EffectiveType,
		Configured: config.Configured, BaseURL: config.EffectiveBaseURL,
	}
}

func (m *Manager) validateSourceID(sourceID string) (string, error) {
	catalog, _, err := m.store.ScheduledReportSources()
	if err != nil {
		return "", err
	}
	sourceID = effectiveSourceID(sourceID, catalog)
	if sourceID == store.ScheduledReportGlobalSourceID || sourceIndex(catalog, sourceID) >= 0 {
		return sourceID, nil
	}
	return "", invalid("目标源站不存在")
}

func (m *Manager) resolveSource(sourceID string) (reportSource, error) {
	catalog, _, err := m.store.ScheduledReportSources()
	if err != nil {
		return nil, err
	}
	sourceID = effectiveSourceID(sourceID, catalog)
	if sourceID == store.ScheduledReportGlobalSourceID {
		if err := m.client.Ready(); err != nil {
			return nil, err
		}
		return m.client, nil
	}
	index := sourceIndex(catalog, sourceID)
	if index < 0 || !customSourceConfigured(catalog.Sources[index]) {
		return nil, ErrSourceNotConfigured
	}
	settings := catalog.Sources[index]
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

func (m *Manager) sourceUsedBy(sourceID string, catalog store.ScheduledReportSourceCatalog) ([]string, error) {
	var usedBy []string
	if report, exists, err := m.store.ScheduledReport(store.ScheduledReportChannelUsage); err != nil {
		return nil, err
	} else if exists {
		config, err := decodeStoredConfig(report.ConfigJSON)
		if err != nil {
			return nil, err
		}
		if effectiveSourceID(config.SourceID, catalog) == sourceID {
			usedBy = append(usedBy, "渠道使用报告")
		}
	}
	if report, exists, err := m.store.ScheduledReport(store.ScheduledReportDaily); err != nil {
		return nil, err
	} else if exists {
		config, err := decodeDailyStoredConfig(report.ConfigJSON)
		if err != nil {
			return nil, err
		}
		if effectiveSourceID(config.SourceID, catalog) == sourceID {
			usedBy = append(usedBy, "每日报告")
		}
	}
	return usedBy, nil
}

func effectiveSourceID(sourceID string, catalog store.ScheduledReportSourceCatalog) string {
	if sourceID = strings.TrimSpace(sourceID); sourceID != "" {
		return sourceID
	}
	if sourceID = strings.TrimSpace(catalog.DefaultSourceID); sourceID != "" {
		return sourceID
	}
	return store.ScheduledReportGlobalSourceID
}

func sourceIndex(catalog store.ScheduledReportSourceCatalog, id string) int {
	for i, source := range catalog.Sources {
		if source.ID == id {
			return i
		}
	}
	return -1
}

func nextSourceID(catalog *store.ScheduledReportSourceCatalog) string {
	for {
		id := fmt.Sprintf("source-%d", catalog.NextID)
		catalog.NextID++
		if sourceIndex(*catalog, id) < 0 {
			return id
		}
	}
}
