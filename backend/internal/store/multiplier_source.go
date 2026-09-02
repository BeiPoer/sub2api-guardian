package store

import "strings"

const metaMultiplierSource = "multiplier_source"

// MultiplierSourceMode 是渠道管理倍率的来源。
type MultiplierSourceMode string

const (
	MultiplierSourceLocal  MultiplierSourceMode = "local"
	MultiplierSourceRemote MultiplierSourceMode = "remote"
)

// RemoteLinkedAccount 记录远程源负责的本地账号和名称归属。
// GeneratedName 用于只清理由 Guardian 生成的后缀，不覆盖用户手工改名。
type RemoteLinkedAccount struct {
	Fingerprint   string `json:"fingerprint"`
	GeneratedName string `json:"generated_name"`
}

// MultiplierSourceSettings 是 G2 的倍率同步源配置与运行状态。
// AccessToken 和 RemoteAccounts 只在服务端使用，接口响应不会序列化它们。
type MultiplierSourceSettings struct {
	Mode           MultiplierSourceMode           `json:"mode"`
	BaseURL        string                         `json:"base_url"`
	Username       string                         `json:"username"`
	AccessToken    string                         `json:"-"`
	TimeoutSeconds int                            `json:"timeout_seconds"`
	SourceID       string                         `json:"source_id"`
	LastRevision   string                         `json:"last_revision"`
	LastState      string                         `json:"last_state"`
	LastComplete   bool                           `json:"last_complete"`
	LastSuccessAt  string                         `json:"last_success_at"`
	LastError      string                         `json:"last_error"`
	LastMatched    int                            `json:"last_matched"`
	LastTotal      int                            `json:"last_total"`
	RemoteAccounts map[string]RemoteLinkedAccount `json:"-"`
}

// multiplierSourceRecord 只用于数据库 JSON；AccessToken 不应出现在公开配置模型里。
type multiplierSourceRecord struct {
	Mode           MultiplierSourceMode           `json:"mode"`
	BaseURL        string                         `json:"base_url"`
	Username       string                         `json:"username"`
	AccessToken    string                         `json:"access_token"`
	TimeoutSeconds int                            `json:"timeout_seconds"`
	SourceID       string                         `json:"source_id"`
	LastRevision   string                         `json:"last_revision"`
	LastState      string                         `json:"last_state"`
	LastComplete   bool                           `json:"last_complete"`
	LastSuccessAt  string                         `json:"last_success_at"`
	LastError      string                         `json:"last_error"`
	LastMatched    int                            `json:"last_matched"`
	LastTotal      int                            `json:"last_total"`
	RemoteAccounts map[string]RemoteLinkedAccount `json:"remote_accounts"`
}

func DefaultMultiplierSourceSettings() MultiplierSourceSettings {
	return MultiplierSourceSettings{
		Mode:           MultiplierSourceLocal,
		TimeoutSeconds: 10,
		RemoteAccounts: map[string]RemoteLinkedAccount{},
	}
}

func normalizeMultiplierSourceSettings(settings *MultiplierSourceSettings) {
	if settings.Mode != MultiplierSourceRemote {
		settings.Mode = MultiplierSourceLocal
	}
	settings.BaseURL = strings.TrimRight(strings.TrimSpace(settings.BaseURL), "/")
	settings.Username = strings.TrimSpace(settings.Username)
	settings.AccessToken = strings.TrimSpace(settings.AccessToken)
	settings.SourceID = strings.TrimSpace(settings.SourceID)
	settings.LastRevision = strings.TrimSpace(settings.LastRevision)
	settings.LastState = strings.TrimSpace(settings.LastState)
	settings.LastError = strings.TrimSpace(settings.LastError)
	if settings.TimeoutSeconds <= 0 {
		settings.TimeoutSeconds = 10
	}
	if settings.TimeoutSeconds > 120 {
		settings.TimeoutSeconds = 120
	}
	if settings.RemoteAccounts == nil {
		settings.RemoteAccounts = map[string]RemoteLinkedAccount{}
	}
}

// MultiplierSourceSettings 读取倍率源配置；未配置时使用本地渠道管理。
func (s *Store) MultiplierSourceSettings() (MultiplierSourceSettings, bool, error) {
	settings := DefaultMultiplierSourceSettings()
	var record multiplierSourceRecord
	if err := s.getJSON(metaMultiplierSource, &record); err != nil {
		if IsNotFound(err) {
			return settings, false, nil
		}
		return MultiplierSourceSettings{}, false, err
	}
	settings = MultiplierSourceSettings{
		Mode: record.Mode, BaseURL: record.BaseURL, Username: record.Username,
		AccessToken: record.AccessToken, TimeoutSeconds: record.TimeoutSeconds,
		SourceID: record.SourceID, LastRevision: record.LastRevision, LastState: record.LastState,
		LastComplete: record.LastComplete, LastSuccessAt: record.LastSuccessAt, LastError: record.LastError,
		LastMatched: record.LastMatched, LastTotal: record.LastTotal, RemoteAccounts: record.RemoteAccounts,
	}
	normalizeMultiplierSourceSettings(&settings)
	return settings, true, nil
}

// SaveMultiplierSourceSettings 写入倍率源配置。
func (s *Store) SaveMultiplierSourceSettings(settings MultiplierSourceSettings) (MultiplierSourceSettings, error) {
	normalizeMultiplierSourceSettings(&settings)
	record := multiplierSourceRecord{
		Mode: settings.Mode, BaseURL: settings.BaseURL, Username: settings.Username,
		AccessToken: settings.AccessToken, TimeoutSeconds: settings.TimeoutSeconds,
		SourceID: settings.SourceID, LastRevision: settings.LastRevision, LastState: settings.LastState,
		LastComplete: settings.LastComplete, LastSuccessAt: settings.LastSuccessAt, LastError: settings.LastError,
		LastMatched: settings.LastMatched, LastTotal: settings.LastTotal, RemoteAccounts: settings.RemoteAccounts,
	}
	s.mu.Lock()
	err := s.setJSON(metaMultiplierSource, record)
	s.mu.Unlock()
	if err != nil {
		return MultiplierSourceSettings{}, err
	}
	return settings, nil
}
