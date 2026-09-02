package engine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/channelmanager"
	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
	"sub2api-guardian/backend/internal/store"
)

const (
	// MultiplierSourceProtocol is the wire-format version shared by G1 and G2.
	MultiplierSourceProtocol = 1
	multiplierSourceMaxItems = 10000
)

// MultiplierSourceScope is the narrow session scope used by G1's read-only API.
const MultiplierSourceScope = "multiplier_read"

// MultiplierSourceSessionTTL bounds a token issued from an existing Guardian password.
const MultiplierSourceSessionTTL = 90 * 24 * time.Hour

// MultiplierSourceStatus 是 G1 对外暴露的非敏感状态。
type MultiplierSourceStatus struct {
	Protocol      int    `json:"protocol"`
	SourceID      string `json:"source_id"`
	State         string `json:"state"`
	Revision      string `json:"revision"`
	Complete      bool   `json:"complete"`
	IndexedTokens int    `json:"indexed_tokens"`
	LastSyncAt    string `json:"last_sync_at,omitempty"`
}

// MultiplierSourceItem 是一次倍率解析的非敏感结果。
type MultiplierSourceItem struct {
	Fingerprint string  `json:"fingerprint"`
	Multiplier  float64 `json:"multiplier"`
	UpdatedAt   string  `json:"updated_at,omitempty"`
}

// MultiplierSourceResolveRequest 是 G2 发给 G1 的指纹查询。
type MultiplierSourceResolveRequest struct {
	Protocol     int      `json:"protocol"`
	Fingerprints []string `json:"fingerprints"`
}

// MultiplierSourceResolveResponse 是 G1 返回的倍率快照。
type MultiplierSourceResolveResponse struct {
	Protocol      int                    `json:"protocol"`
	SourceID      string                 `json:"source_id"`
	State         string                 `json:"state"`
	Revision      string                 `json:"revision"`
	Complete      bool                   `json:"complete"`
	IndexedTokens int                    `json:"indexed_tokens"`
	LastSyncAt    string                 `json:"last_sync_at,omitempty"`
	Items         []MultiplierSourceItem `json:"items"`
}

// MultiplierSourceAuthorization 是 G1 授权接口的结果。
type MultiplierSourceAuthorization struct {
	AccessToken string `json:"access_token"`
	SourceID    string `json:"source_id"`
	ExpiresAt   string `json:"expires_at"`
}

// RemoteMultiplierSyncResult 是 G2 一次远程同步的非敏感结果。
type RemoteMultiplierSyncResult struct {
	State    string `json:"state"`
	Revision string `json:"revision"`
	Complete bool   `json:"complete"`
	Matched  int    `json:"matched"`
	Total    int    `json:"total"`
	Changed  bool   `json:"changed"`
}

type multiplierSourceIndex struct {
	status MultiplierSourceStatus
	items  map[string]MultiplierSourceItem
}

// MultiplierSourceStatus 返回当前 Guardian 可供其它实例读取的倍率源状态。
func (e *Engine) MultiplierSourceStatus() (MultiplierSourceStatus, error) {
	index, err := e.buildMultiplierSourceIndex("status")
	if err != nil {
		return MultiplierSourceStatus{}, err
	}
	return index.status, nil
}

// ResolveMultiplierSource 按指纹解析 G1 的渠道管理倍率。
// secret 只用于 HMAC，原始凭据永远不会进入响应。
func (e *Engine) ResolveMultiplierSource(secret string, fingerprints []string) (MultiplierSourceResolveResponse, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return MultiplierSourceResolveResponse{}, errors.New("倍率源授权无效")
	}
	if len(fingerprints) > multiplierSourceMaxItems {
		return MultiplierSourceResolveResponse{}, errors.New("倍率源请求项目过多")
	}
	index, err := e.buildMultiplierSourceIndex(secret)
	if err != nil {
		return MultiplierSourceResolveResponse{}, err
	}
	seen := make(map[string]struct{}, len(fingerprints))
	items := make([]MultiplierSourceItem, 0, len(fingerprints))
	for _, fingerprint := range fingerprints {
		fingerprint = strings.TrimSpace(fingerprint)
		if fingerprint == "" {
			continue
		}
		if _, exists := seen[fingerprint]; exists {
			continue
		}
		seen[fingerprint] = struct{}{}
		if item, ok := index.items[fingerprint]; ok {
			items = append(items, item)
		}
	}
	return MultiplierSourceResolveResponse{
		Protocol: MultiplierSourceProtocol, SourceID: index.status.SourceID,
		State: index.status.State, Revision: index.status.Revision,
		Complete: index.status.Complete, IndexedTokens: index.status.IndexedTokens,
		LastSyncAt: index.status.LastSyncAt, Items: items,
	}, nil
}

// buildMultiplierSourceIndex 从 G1 已有渠道管理缓存生成只读索引。
// 缓存不完整时仍返回已知项目，但 Complete=false，消费者不得据此清理旧值。
func (e *Engine) buildMultiplierSourceIndex(secret string) (multiplierSourceIndex, error) {
	channels, err := e.store.UpstreamChannels()
	if err != nil {
		return multiplierSourceIndex{}, err
	}
	items := make(map[string]MultiplierSourceItem)
	parts := make([]string, 0)
	complete := true
	seenSupported := false
	var lastSync time.Time
	conflict := false
	conflictedFingerprints := make(map[string]struct{})
	for _, channel := range channels {
		if channel.Ignored || !supportsLinkedUpstreamType(channel.Type) {
			continue
		}
		seenSupported = true
		groups, groupsErr := e.store.UpstreamCache(channel.ID, "groups")
		tokens, tokensErr := e.store.UpstreamCache(channel.ID, "tokens")
		if groupsErr != nil || tokensErr != nil || !groups.Exists || !tokens.Exists || channel.Status != "active" {
			complete = false
		}
		groupStamp := groups.SyncedAt
		tokenStamp := tokens.SyncedAt
		if t := parseSourceTime(groupStamp); t.After(lastSync) {
			lastSync = t
		}
		if t := parseSourceTime(tokenStamp); t.After(lastSync) {
			lastSync = t
		}
		parts = append(parts, fmt.Sprintf("channel:%d|%s|%s|%s|%g|%s|%s",
			channel.ID, channel.Type, channel.BaseURL, channel.Status,
			channel.RechargeRatio, groupStamp, tokenStamp))
		if groupsErr != nil || tokensErr != nil || !groups.Exists || !tokens.Exists || groups.Value == nil || tokens.Value == nil {
			continue
		}
		extraction := channelmanager.TokenMultiplierLinkCandidatesForLink(groups.Value, tokens.Value, channel.Type)
		rawRatios := extraction.Ratios
		if extraction.Conflicts > 0 || extraction.Incomplete {
			complete = false
		}
		recharge := channel.RechargeRatio
		if !validLinkedMultiplier(recharge) {
			recharge = 1
		}
		keys := make([]string, 0, len(rawRatios))
		for key := range rawRatios {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			ratio := rawRatios[key] / recharge
			if !validLinkedMultiplier(ratio) {
				complete = false
				continue
			}
			key = sourceLinkedKey(channel.Type, key)
			fingerprint, ok := LinkedCredentialFingerprint(secret, channel.BaseURL, key)
			if !ok {
				complete = false
				continue
			}
			item := MultiplierSourceItem{Fingerprint: fingerprint, Multiplier: ratio, UpdatedAt: latestStamp(groupStamp, tokenStamp)}
			if previous, exists := items[fingerprint]; exists && previous.Multiplier != ratio {
				delete(items, fingerprint)
				conflictedFingerprints[fingerprint] = struct{}{}
				conflict = true
				continue
			}
			if _, conflicted := conflictedFingerprints[fingerprint]; !conflicted {
				items[fingerprint] = item
			}
			parts = append(parts, fmt.Sprintf("item:%s|%g", linkedIdentityRevision(channel.BaseURL, key), ratio))
		}
	}
	if !seenSupported {
		complete = false
	}
	if conflict {
		complete = false
	}
	sort.Strings(parts)
	hash := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	state := "ready"
	if !seenSupported {
		state = "not_ready"
	} else if !complete {
		state = "partial"
	}
	status := MultiplierSourceStatus{
		Protocol:      MultiplierSourceProtocol,
		SourceID:      "guardian-multiplier-source",
		State:         state,
		Revision:      hex.EncodeToString(hash[:]),
		Complete:      complete,
		IndexedTokens: len(items),
	}
	if !lastSync.IsZero() {
		status.LastSyncAt = lastSync.UTC().Format(time.RFC3339Nano)
	}
	return multiplierSourceIndex{status: status, items: items}, nil
}

// LinkedCredentialFingerprint 用统一规则生成 URL + Key 的不可逆指纹。
func LinkedCredentialFingerprint(secret, baseURL, apiKey string) (string, bool) {
	secret = strings.TrimSpace(secret)
	apiKey = strings.TrimSpace(apiKey)
	if secret == "" || apiKey == "" || strings.Contains(apiKey, "*") {
		return "", false
	}
	canonicalURL, ok := normalizeLinkedURL(baseURL)
	if !ok {
		return "", false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonicalURL + "\x00" + apiKey))
	return hex.EncodeToString(mac.Sum(nil)), true
}

func linkedIdentityRevision(baseURL, apiKey string) string {
	canonicalURL, ok := normalizeLinkedURL(baseURL)
	if !ok {
		return ""
	}
	hash := sha256.Sum256([]byte(canonicalURL + "\x00" + strings.TrimSpace(apiKey)))
	return hex.EncodeToString(hash[:])
}

func sourceLinkedKey(channelType store.UpstreamChannelType, key string) string {
	key = strings.TrimSpace(key)
	if channelType == store.UpstreamChannelNewAPI && key != "" && !strings.HasPrefix(key, "sk-") {
		return "sk-" + key
	}
	return key
}

func sourceLinkedKeyVariants(key string) []string {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	variants := []string{key}
	if strings.HasPrefix(key, "sk-") {
		variants = append(variants, strings.TrimPrefix(key, "sk-"))
	} else {
		variants = append(variants, "sk-"+key)
	}
	return variants
}

func parseSourceTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	if value, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return value
	}
	value, _ := time.Parse(time.RFC3339, raw)
	return value
}

func latestStamp(a, b string) string {
	if parseSourceTime(b).After(parseSourceTime(a)) {
		return b
	}
	return a
}

// AuthorizeRemoteMultiplierSource 使用 G1 现有网站账号换取窄权限令牌。
func AuthorizeRemoteMultiplierSource(ctx context.Context, baseURL, username, password string, timeout time.Duration) (MultiplierSourceAuthorization, error) {
	baseURL, err := normalizeRemoteMultiplierURL(baseURL)
	if err != nil {
		return MultiplierSourceAuthorization{}, err
	}
	if strings.TrimSpace(username) == "" || password == "" {
		return MultiplierSourceAuthorization{}, errors.New("G1 用户名和密码不能为空")
	}
	timeout = normalizeMultiplierSourceTimeout(timeout)
	var result MultiplierSourceAuthorization
	if err := doMultiplierSourceJSON(ctx, baseURL, timeout, http.MethodPost,
		"/internal/v1/multiplier-source/authorize", "", map[string]any{
			"username": username, "password": password,
		}, &result); err != nil {
		return MultiplierSourceAuthorization{}, err
	}
	if strings.TrimSpace(result.AccessToken) == "" {
		return MultiplierSourceAuthorization{}, errors.New("G1 未返回有效授权")
	}
	return result, nil
}

// FetchRemoteMultiplierSourceStatus 查询 G1 的非敏感状态。
func FetchRemoteMultiplierSourceStatus(ctx context.Context, settings store.MultiplierSourceSettings) (MultiplierSourceStatus, error) {
	baseURL, err := normalizeRemoteMultiplierURL(settings.BaseURL)
	if err != nil {
		return MultiplierSourceStatus{}, err
	}
	var result MultiplierSourceStatus
	if err := doMultiplierSourceJSON(ctx, baseURL, time.Duration(settings.TimeoutSeconds)*time.Second,
		http.MethodGet, "/internal/v1/multiplier-source/status", settings.AccessToken, nil, &result); err != nil {
		return MultiplierSourceStatus{}, err
	}
	if result.Protocol != MultiplierSourceProtocol {
		return MultiplierSourceStatus{}, errors.New("G1 倍率源协议版本不兼容")
	}
	return result, nil
}

func fetchRemoteMultiplierSource(ctx context.Context, settings store.MultiplierSourceSettings, fingerprints []string) (MultiplierSourceResolveResponse, error) {
	baseURL, err := normalizeRemoteMultiplierURL(settings.BaseURL)
	if err != nil {
		return MultiplierSourceResolveResponse{}, err
	}
	var result MultiplierSourceResolveResponse
	if err := doMultiplierSourceJSON(ctx, baseURL, time.Duration(settings.TimeoutSeconds)*time.Second,
		http.MethodPost, "/internal/v1/multiplier-source/resolve", settings.AccessToken,
		MultiplierSourceResolveRequest{Protocol: MultiplierSourceProtocol, Fingerprints: fingerprints}, &result); err != nil {
		return MultiplierSourceResolveResponse{}, err
	}
	if result.Protocol != MultiplierSourceProtocol {
		return MultiplierSourceResolveResponse{}, errors.New("G1 倍率源协议版本不兼容")
	}
	if len(result.Items) > len(fingerprints) {
		return MultiplierSourceResolveResponse{}, errors.New("G1 返回了未请求的倍率项目")
	}
	requested := make(map[string]struct{}, len(fingerprints))
	for _, fingerprint := range fingerprints {
		requested[fingerprint] = struct{}{}
	}
	seen := make(map[string]struct{}, len(result.Items))
	for _, item := range result.Items {
		if _, ok := requested[item.Fingerprint]; !ok {
			return MultiplierSourceResolveResponse{}, errors.New("G1 返回了未请求的倍率项目")
		}
		if _, duplicate := seen[item.Fingerprint]; duplicate {
			return MultiplierSourceResolveResponse{}, errors.New("G1 重复返回倍率项目")
		}
		seen[item.Fingerprint] = struct{}{}
		if !validLinkedMultiplier(item.Multiplier) {
			return MultiplierSourceResolveResponse{}, errors.New("G1 返回了非法倍率")
		}
	}
	if result.Complete && result.State != "ready" {
		return MultiplierSourceResolveResponse{}, errors.New("G1 完整快照状态无效")
	}
	return result, nil
}

func normalizeRemoteMultiplierURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err == nil {
		parsed.Scheme = strings.ToLower(parsed.Scheme)
	}
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", errors.New("G1 地址必须是有效的 HTTP(S) 绝对地址")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("G1 地址不能包含查询参数或片段")
	}
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
	return strings.TrimRight(parsed.String(), "/"), nil
}

// NormalizeRemoteMultiplierURL validates and normalizes a configured G1 URL.
func NormalizeRemoteMultiplierURL(raw string) (string, error) {
	return normalizeRemoteMultiplierURL(raw)
}

func doMultiplierSourceJSON(ctx context.Context, baseURL string, timeout time.Duration, method, path, token string, body, out any) error {
	timeout = normalizeMultiplierSourceTimeout(timeout)
	client := &http.Client{Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求 G1 倍率源失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return errors.New("读取 G1 倍率源响应失败")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusUnauthorized {
			return errors.New("G1 倍率源授权已失效")
		}
		return fmt.Errorf("G1 倍率源返回 HTTP %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errors.New("G1 倍率源响应格式无效")
	}
	return nil
}

func normalizeMultiplierSourceTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return 10 * time.Second
	}
	if timeout > 120*time.Second {
		return 120 * time.Second
	}
	return timeout
}

// SyncConfiguredMultiplierSource 按 G2 当前配置同步远程 G1 倍率。
// force=true 用于页面的“立即同步”，后台心跳会使用 false。
func (e *Engine) SyncConfiguredMultiplierSource(ctx context.Context, force bool) (RemoteMultiplierSyncResult, error) {
	settings, _, err := e.store.MultiplierSourceSettings()
	if err != nil {
		return RemoteMultiplierSyncResult{}, err
	}
	if settings.Mode != store.MultiplierSourceRemote {
		return RemoteMultiplierSyncResult{State: "local"}, nil
	}
	interval := e.remoteMultiplierInterval()
	e.remoteMultiplierMu.Lock()
	if !force && !e.lastRemoteMultiplierAttempt.IsZero() && time.Since(e.lastRemoteMultiplierAttempt) < interval {
		e.remoteMultiplierMu.Unlock()
		return remoteResultFromSettings(settings), nil
	}
	e.lastRemoteMultiplierAttempt = time.Now()
	e.remoteMultiplierMu.Unlock()

	e.linkedMultiplierMu.Lock()
	defer e.linkedMultiplierMu.Unlock()
	// 配置切换和后台同步可能并发发生；锁内重新读取，避免用切换前的快照
	// 把新配置或新授权令牌覆盖回数据库。
	settings, _, err = e.store.MultiplierSourceSettings()
	if err != nil {
		return RemoteMultiplierSyncResult{}, err
	}
	if settings.Mode != store.MultiplierSourceRemote {
		return RemoteMultiplierSyncResult{State: "local"}, nil
	}
	if strings.TrimSpace(settings.AccessToken) == "" || strings.TrimSpace(settings.BaseURL) == "" {
		return e.recordRemoteMultiplierError(settings, errors.New("G1 倍率源尚未授权"))
	}
	accounts, err := e.store.Accounts()
	if err != nil {
		return e.recordRemoteMultiplierError(settings, err)
	}
	credentialsByID, err := e.linkedCredentials(ctx)
	if err != nil {
		return e.recordRemoteMultiplierError(settings, err)
	}
	fingerprintAccounts := make(map[string][]linkedAccount)
	fingerprints := make([]string, 0)
	seenFingerprints := make(map[string]struct{})
	total := 0
	localSnapshotComplete := true
	for _, account := range accounts {
		if !policy.IsAPIKeyType(account.Type) {
			continue
		}
		total++
		credentials, ok := credentialsByID[account.ID]
		if !ok {
			localSnapshotComplete = false
			continue
		}
		accountHasFingerprint := false
		for _, key := range sourceLinkedKeyVariants(credentials.APIKey) {
			fingerprint, valid := LinkedCredentialFingerprint(settings.AccessToken, credentials.BaseURL, key)
			if !valid {
				continue
			}
			accountHasFingerprint = true
			fingerprintAccounts[fingerprint] = append(fingerprintAccounts[fingerprint], linkedAccount{
				account: account, fingerprint: fingerprint,
			})
			if _, seen := seenFingerprints[fingerprint]; !seen {
				seenFingerprints[fingerprint] = struct{}{}
				fingerprints = append(fingerprints, fingerprint)
			}
		}
		if !accountHasFingerprint {
			localSnapshotComplete = false
		}
	}
	sort.Strings(fingerprints)
	response, err := fetchRemoteMultiplierSource(ctx, settings, fingerprints)
	if err != nil {
		return e.recordRemoteMultiplierError(settings, err)
	}
	matchedByID := make(map[int64]linkedAccount)
	conflictedAccounts := make(map[int64]struct{})
	for _, item := range response.Items {
		if !validLinkedMultiplier(item.Multiplier) {
			continue
		}
		candidates := fingerprintAccounts[item.Fingerprint]
		for _, candidate := range candidates {
			if _, conflicted := conflictedAccounts[candidate.account.ID]; conflicted {
				continue
			}
			if previous, exists := matchedByID[candidate.account.ID]; exists && previous.ratio != item.Multiplier {
				delete(matchedByID, candidate.account.ID)
				conflictedAccounts[candidate.account.ID] = struct{}{}
				localSnapshotComplete = false
				continue
			}
			candidate.ratio = item.Multiplier
			candidate.fingerprint = item.Fingerprint
			matchedByID[candidate.account.ID] = candidate
		}
	}
	matched := make([]linkedAccount, 0, len(matchedByID))
	for _, item := range matchedByID {
		matched = append(matched, item)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].account.ID < matched[j].account.ID })
	applyResult, err := e.applyLinkedMultiplierMatches(ctx, matched, "G1 渠道管理")
	if err != nil {
		return e.recordRemoteMultiplierError(settings, err)
	}

	owned := cloneRemoteLinkedAccounts(settings.RemoteAccounts)
	for _, item := range matched {
		key := itoa(item.account.ID)
		generated := applyResult.generatedName[key]
		if generated == "" {
			generated = owned[key].GeneratedName
		}
		owned[key] = store.RemoteLinkedAccount{Fingerprint: item.fingerprint, GeneratedName: generated}
	}
	// 只有 G1 快照完整且 G2 本地凭据快照也完整时才允许清理旧归属；
	// 凭据缺失或变体冲突都可能只是暂时读不到，宁可延后一轮。
	if response.Complete && localSnapshotComplete {
		if changed, cleanupErr := e.cleanupRemoteLinkedAccounts(ctx, owned, matchedByID); cleanupErr != nil {
			return e.recordRemoteMultiplierError(settings, cleanupErr)
		} else if changed {
			applyResult.changed = true
		}
	}
	effectiveComplete := response.Complete && localSnapshotComplete
	effectiveState := response.State
	if response.Complete && !localSnapshotComplete {
		effectiveState = "partial"
	}
	settings.SourceID = response.SourceID
	settings.LastRevision = response.Revision
	settings.LastState = effectiveState
	settings.LastComplete = effectiveComplete
	settings.LastSuccessAt = time.Now().UTC().Format(time.RFC3339Nano)
	settings.LastError = ""
	settings.LastMatched = len(matched)
	settings.LastTotal = total
	settings.RemoteAccounts = owned
	if _, err := e.store.SaveMultiplierSourceSettings(settings); err != nil {
		return RemoteMultiplierSyncResult{}, err
	}
	return RemoteMultiplierSyncResult{
		State: effectiveState, Revision: response.Revision, Complete: effectiveComplete,
		Matched: len(matched), Total: total, Changed: applyResult.changed,
	}, nil
}

// DetachRemoteMultiplierSource 清理当前远程源曾经负责的联动倍率和名称后缀。
func (e *Engine) DetachRemoteMultiplierSource(ctx context.Context) error {
	e.linkedMultiplierMu.Lock()
	defer e.linkedMultiplierMu.Unlock()
	// 与同步一样，锁内重读以避免清理使用配置切换前的旧归属快照。
	settings, _, err := e.store.MultiplierSourceSettings()
	if err != nil {
		return err
	}
	owned := cloneRemoteLinkedAccounts(settings.RemoteAccounts)
	if _, err := e.cleanupRemoteLinkedAccounts(ctx, owned, nil); err != nil {
		return err
	}
	settings.RemoteAccounts = owned
	if _, err := e.store.SaveMultiplierSourceSettings(settings); err != nil {
		return err
	}
	if len(owned) > 0 {
		return fmt.Errorf("仍有 %d 个远程联动账号名称未能清理，请稍后重试", len(owned))
	}
	return nil
}

// UpdateMultiplierSourceSettings 在同一把联动锁内读取、清理旧归属并保存新配置。
// 配置页面的切换/授权操作通过它提交，避免后台同步在清理和落库之间插入旧快照。
func (e *Engine) UpdateMultiplierSourceSettings(
	ctx context.Context,
	update func(*store.MultiplierSourceSettings) (detach bool, err error),
) (store.MultiplierSourceSettings, error) {
	e.linkedMultiplierMu.Lock()
	defer e.linkedMultiplierMu.Unlock()

	settings, _, err := e.store.MultiplierSourceSettings()
	if err != nil {
		return store.MultiplierSourceSettings{}, err
	}
	originalMode := settings.Mode
	owned := cloneRemoteLinkedAccounts(settings.RemoteAccounts)
	detach, err := update(&settings)
	if err != nil {
		return store.MultiplierSourceSettings{}, err
	}
	if originalMode == store.MultiplierSourceLocal && settings.Mode == store.MultiplierSourceRemote {
		seeded, seedErr := e.seedRemoteLinkedAccounts()
		if seedErr != nil {
			return store.MultiplierSourceSettings{}, seedErr
		}
		if settings.RemoteAccounts == nil {
			settings.RemoteAccounts = make(map[string]store.RemoteLinkedAccount)
		}
		for key, record := range seeded {
			if _, exists := settings.RemoteAccounts[key]; !exists {
				settings.RemoteAccounts[key] = record
			}
		}
	}
	if detach {
		if _, err := e.cleanupRemoteLinkedAccounts(ctx, owned, nil); err != nil {
			return store.MultiplierSourceSettings{}, err
		}
		if len(owned) > 0 {
			return store.MultiplierSourceSettings{}, fmt.Errorf("仍有 %d 个远程联动账号名称未能清理，请稍后重试", len(owned))
		}
	}
	saved, err := e.store.SaveMultiplierSourceSettings(settings)
	if err != nil {
		return store.MultiplierSourceSettings{}, err
	}
	return saved, nil
}

// seedRemoteLinkedAccounts 把切换前本地渠道管理产生的联动值登记为待清理归属。
// 远程源返回完整快照后，cleanupRemoteLinkedAccounts 会移除未匹配项；在快照不完整
// 时继续保留旧值，避免一次 G1 抖动把渠道池倍率清空。
func (e *Engine) seedRemoteLinkedAccounts() (map[string]store.RemoteLinkedAccount, error) {
	policyState, err := e.store.Policy()
	if err != nil {
		return nil, err
	}
	if len(policyState.AccountLinkedMultipliers) == 0 {
		return map[string]store.RemoteLinkedAccount{}, nil
	}
	accounts, err := e.store.Accounts()
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]domain.Account, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	seeded := make(map[string]store.RemoteLinkedAccount, len(policyState.AccountLinkedMultipliers))
	for accountID := range policyState.AccountLinkedMultipliers {
		record := store.RemoteLinkedAccount{}
		if id, parseErr := parseID(accountID); parseErr == nil {
			if account, ok := byID[id]; ok && linkedMultiplierBaseName(account.Name) != account.Name {
				record.GeneratedName = account.Name
			}
		}
		seeded[accountID] = record
	}
	return seeded, nil
}

func (e *Engine) remoteMultiplierInterval() time.Duration {
	p, err := e.store.Policy()
	if err != nil || p.UpstreamMultiplier.IntervalSeconds < 30 {
		return 30 * time.Second
	}
	return time.Duration(p.UpstreamMultiplier.IntervalSeconds) * time.Second
}

func remoteResultFromSettings(settings store.MultiplierSourceSettings) RemoteMultiplierSyncResult {
	return RemoteMultiplierSyncResult{
		State: settings.LastState, Revision: settings.LastRevision,
		Complete: settings.LastComplete, Matched: settings.LastMatched, Total: settings.LastTotal,
	}
}

func (e *Engine) recordRemoteMultiplierError(settings store.MultiplierSourceSettings, err error) (RemoteMultiplierSyncResult, error) {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "G1 倍率源同步失败"
	}
	settings.LastState = "error"
	settings.LastError = message
	_, _ = e.store.SaveMultiplierSourceSettings(settings)
	return remoteResultFromSettings(settings), err
}

func cloneRemoteLinkedAccounts(input map[string]store.RemoteLinkedAccount) map[string]store.RemoteLinkedAccount {
	out := make(map[string]store.RemoteLinkedAccount, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func (e *Engine) cleanupRemoteLinkedAccounts(ctx context.Context, owned map[string]store.RemoteLinkedAccount, matched map[int64]linkedAccount) (bool, error) {
	remove := make(map[string]struct{})
	changed := false
	for accountID, record := range owned {
		id, err := parseID(accountID)
		if err != nil {
			delete(owned, accountID)
			remove[accountID] = struct{}{}
			changed = true
			continue
		}
		if _, stillMatched := matched[id]; stillMatched {
			continue
		}
		account, accountErr := e.store.Account(id)
		if accountErr != nil {
			remove[accountID] = struct{}{}
			delete(owned, accountID)
			changed = true
			continue
		}
		if record.GeneratedName == "" || account.Name != record.GeneratedName {
			remove[accountID] = struct{}{}
			delete(owned, accountID)
			changed = true
			continue
		}
		baseName := linkedMultiplierBaseName(account.Name)
		if baseName == account.Name {
			remove[accountID] = struct{}{}
			delete(owned, accountID)
			changed = true
			continue
		}
		// 快照已经确认该账号不再由远程源负责，先移除调度倍率；
		// 名称写回失败时保留归属记录，下一轮继续重试清理后缀。
		remove[accountID] = struct{}{}
		changed = true
		if err := e.client.UpdateAccount(ctx, id, map[string]any{"name": baseName}); err != nil {
			continue
		}
		account.Name = baseName
		_ = e.store.UpsertAccount(account)
		delete(owned, accountID)
	}
	if removed, err := e.store.RemoveAccountLinkedMultipliers(remove); err != nil {
		return changed, err
	} else if removed {
		if err := e.refreshGroupStates(); err != nil {
			e.store.Log("warn", "upstream_multiplier_link_refresh_failed", nil, nil,
				fmt.Sprintf("远程联动倍率已清理，但分组聚合刷新失败: %s", err), nil)
		}
		e.fireNotify()
		changed = true
	}
	return changed, nil
}

func parseID(value string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid account id")
	}
	return id, nil
}

func linkedMultiplierBaseName(name string) string {
	name = strings.TrimSpace(name)
	index := strings.LastIndex(name, linkedMultiplierMarker)
	if index < 0 || !strings.HasSuffix(name, "】") {
		return name
	}
	suffix := name[index+len(linkedMultiplierMarker) : len(name)-len("】")]
	if !validLinkedMultiplierString(suffix) {
		return name
	}
	return name[:index]
}

func validLinkedMultiplierString(value string) bool {
	if value == "" {
		return false
	}
	parsed, err := strconv.ParseFloat(value, 64)
	return err == nil && validLinkedMultiplier(parsed)
}
