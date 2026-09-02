package store

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	metaScheduledReportSource     = "scheduled_report_source"
	ScheduledReportGlobalSourceID = "global"
)

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

// ScheduledReportSource 是定时报告独立保存的一个自定义源站。
type ScheduledReportSource struct {
	ID           string                    `json:"id"`
	Name         string                    `json:"name"`
	SourceType   ScheduledReportSourceType `json:"source_type"`
	BaseURL      string                    `json:"base_url"`
	Credential   string                    `json:"credential"`
	NewAPIUserID int64                     `json:"newapi_user_id"`
}

// ScheduledReportSourceCatalog 保存全部自定义源站。全局连接是固定项，不重复保存凭据。
// DefaultSourceID 只用于兼容尚未写入 source_id 的旧报告配置。
type ScheduledReportSourceCatalog struct {
	Sources         []ScheduledReportSource `json:"sources"`
	DefaultSourceID string                  `json:"default_source_id"`
	NextID          int64                   `json:"next_id"`
}

// ScheduledReportSourceSettings 是旧版单源站存储模型，保留读写兼容。
type ScheduledReportSourceSettings struct {
	Mode         ScheduledReportSourceMode `json:"mode"`
	SourceType   ScheduledReportSourceType `json:"source_type"`
	BaseURL      string                    `json:"base_url"`
	Credential   string                    `json:"credential"`
	NewAPIUserID int64                     `json:"newapi_user_id"`
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

func DefaultScheduledReportSourceCatalog() ScheduledReportSourceCatalog {
	return ScheduledReportSourceCatalog{
		Sources:         []ScheduledReportSource{},
		DefaultSourceID: ScheduledReportGlobalSourceID,
		NextID:          1,
	}
}

func normalizeScheduledReportSource(source *ScheduledReportSource) {
	source.ID = strings.TrimSpace(source.ID)
	source.Name = strings.TrimSpace(source.Name)
	source.BaseURL = strings.TrimRight(strings.TrimSpace(source.BaseURL), "/")
	source.Credential = strings.TrimSpace(source.Credential)
}

func normalizeScheduledReportSourceCatalog(catalog *ScheduledReportSourceCatalog) {
	if catalog.Sources == nil {
		catalog.Sources = []ScheduledReportSource{}
	}
	if strings.TrimSpace(catalog.DefaultSourceID) == "" {
		catalog.DefaultSourceID = ScheduledReportGlobalSourceID
	}
	if catalog.NextID < 1 {
		catalog.NextID = 1
	}
	for i := range catalog.Sources {
		normalizeScheduledReportSource(&catalog.Sources[i])
	}
}

func (s *Store) ScheduledReportSources() (ScheduledReportSourceCatalog, bool, error) {
	raw, err := s.getMeta(metaScheduledReportSource)
	if err != nil {
		if IsNotFound(err) {
			return DefaultScheduledReportSourceCatalog(), false, nil
		}
		return ScheduledReportSourceCatalog{}, false, err
	}

	var shape map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &shape); err != nil {
		return ScheduledReportSourceCatalog{}, false, err
	}
	if _, current := shape["sources"]; current {
		catalog := DefaultScheduledReportSourceCatalog()
		if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
			return ScheduledReportSourceCatalog{}, false, err
		}
		normalizeScheduledReportSourceCatalog(&catalog)
		return catalog, true, nil
	}

	// 旧版只保存一个共享源站。转换时保留备用自定义凭据，并让旧报告继续使用原模式。
	legacy := ScheduledReportSourceSettings{
		Mode: ScheduledReportSourceGlobal, SourceType: ScheduledReportSourceSub2API,
	}
	if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
		return ScheduledReportSourceCatalog{}, false, err
	}
	normalizeScheduledReportSourceSettings(&legacy)
	catalog := DefaultScheduledReportSourceCatalog()
	if legacy.BaseURL != "" || legacy.Credential != "" || legacy.Mode == ScheduledReportSourceCustom {
		source := ScheduledReportSource{
			ID: "source-1", Name: "原自定义源站", SourceType: legacy.SourceType,
			BaseURL: legacy.BaseURL, Credential: legacy.Credential, NewAPIUserID: legacy.NewAPIUserID,
		}
		if source.SourceType == "" {
			source.SourceType = ScheduledReportSourceSub2API
		}
		normalizeScheduledReportSource(&source)
		catalog.Sources = append(catalog.Sources, source)
		catalog.NextID = 2
		if legacy.Mode == ScheduledReportSourceCustom {
			catalog.DefaultSourceID = source.ID
		}
	}
	return catalog, true, nil
}

func (s *Store) SaveScheduledReportSources(catalog ScheduledReportSourceCatalog) (ScheduledReportSourceCatalog, error) {
	normalizeScheduledReportSourceCatalog(&catalog)
	seen := map[string]bool{ScheduledReportGlobalSourceID: true}
	for _, source := range catalog.Sources {
		if source.ID == "" || seen[source.ID] {
			return ScheduledReportSourceCatalog{}, fmt.Errorf("定时报告源站 ID 无效: %s", source.ID)
		}
		seen[source.ID] = true
	}
	s.mu.Lock()
	err := s.setJSON(metaScheduledReportSource, catalog)
	s.mu.Unlock()
	if err != nil {
		return ScheduledReportSourceCatalog{}, err
	}
	return catalog, nil
}

// ScheduledReportSourceSettings 让旧调用方继续看到一个当前源站。
func (s *Store) ScheduledReportSourceSettings() (ScheduledReportSourceSettings, bool, error) {
	catalog, exists, err := s.ScheduledReportSources()
	if err != nil {
		return ScheduledReportSourceSettings{}, false, err
	}
	settings := ScheduledReportSourceSettings{
		Mode: ScheduledReportSourceGlobal, SourceType: ScheduledReportSourceSub2API,
	}
	index := sourceIndexInCatalog(catalog, catalog.DefaultSourceID)
	if index < 0 && len(catalog.Sources) > 0 {
		index = 0
	}
	if index >= 0 {
		source := catalog.Sources[index]
		settings.SourceType = source.SourceType
		settings.BaseURL = source.BaseURL
		settings.Credential = source.Credential
		settings.NewAPIUserID = source.NewAPIUserID
		if catalog.DefaultSourceID == source.ID {
			settings.Mode = ScheduledReportSourceCustom
		}
	}
	return settings, exists, nil
}

// SaveScheduledReportSourceSettings 把旧版单源站写入转换成目录中的一个自定义项。
func (s *Store) SaveScheduledReportSourceSettings(settings ScheduledReportSourceSettings) (ScheduledReportSourceSettings, error) {
	normalizeScheduledReportSourceSettings(&settings)
	catalog, _, err := s.ScheduledReportSources()
	if err != nil {
		return ScheduledReportSourceSettings{}, err
	}
	if settings.Mode == ScheduledReportSourceGlobal {
		catalog.DefaultSourceID = ScheduledReportGlobalSourceID
		if _, err := s.SaveScheduledReportSources(catalog); err != nil {
			return ScheduledReportSourceSettings{}, err
		}
		saved, _, err := s.ScheduledReportSourceSettings()
		return saved, err
	}
	index := sourceIndexInCatalog(catalog, catalog.DefaultSourceID)
	if index < 0 && len(catalog.Sources) > 0 {
		index = 0
	}
	id := "source-1"
	name := "原自定义源站"
	if index >= 0 {
		id = catalog.Sources[index].ID
		name = catalog.Sources[index].Name
	}
	source := ScheduledReportSource{
		ID: id, Name: name, SourceType: settings.SourceType, BaseURL: settings.BaseURL,
		Credential: settings.Credential, NewAPIUserID: settings.NewAPIUserID,
	}
	normalizeScheduledReportSource(&source)
	if index >= 0 {
		catalog.Sources[index] = source
	} else {
		catalog.Sources = append(catalog.Sources, source)
		if catalog.NextID < 2 {
			catalog.NextID = 2
		}
	}
	catalog.DefaultSourceID = source.ID
	if _, err := s.SaveScheduledReportSources(catalog); err != nil {
		return ScheduledReportSourceSettings{}, err
	}
	saved, _, err := s.ScheduledReportSourceSettings()
	return saved, err
}

func sourceIndexInCatalog(catalog ScheduledReportSourceCatalog, id string) int {
	for i, source := range catalog.Sources {
		if source.ID == id {
			return i
		}
	}
	return -1
}
