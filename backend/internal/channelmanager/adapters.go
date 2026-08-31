package channelmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/store"
)

const (
	maxUpstreamResponseBytes  = 20 << 20
	defaultNewAPIQuotaPerUnit = 500000
)

type syncResult struct {
	profile       any
	groups        any
	tokens        []any
	subscriptions any
	raw           map[string]any
}

type quotaConversion struct {
	DisplayType  string  `json:"displayType"`
	QuotaPerUnit float64 `json:"quotaPerUnit"`
	Rate         float64 `json:"rate"`
	Unit         string  `json:"unit"`
}

func NormalizeBaseURL(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", invalid("站点链接不能为空")
	}
	if !strings.HasPrefix(strings.ToLower(trimmed), "http://") && !strings.HasPrefix(strings.ToLower(trimmed), "https://") {
		trimmed = "https://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", invalid("站点链接格式无效")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return strings.TrimRight(parsed.String(), "/"), nil
}

func sub2APIURL(channel store.UpstreamChannel, path string) string {
	return strings.TrimRight(channel.BaseURL, "/") + "/api/v1" + path
}

func newAPIURL(channel store.UpstreamChannel, path string) string {
	return strings.TrimRight(channel.BaseURL, "/") + "/api" + path
}

func (m *Manager) requestJSON(ctx context.Context, method, target string, body any, headers http.Header) (any, int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}

	resp, err := m.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, 0, &Error{Status: http.StatusGatewayTimeout, Message: "上游请求超时"}
		}
		return nil, 0, &Error{Status: http.StatusBadGateway, Message: "上游请求失败：" + err.Error()}
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, maxUpstreamResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, resp.StatusCode, upstreamError(resp.StatusCode, "读取上游响应失败", nil)
	}
	if len(raw) > maxUpstreamResponseBytes {
		return nil, resp.StatusCode, upstreamError(http.StatusBadGateway, "上游响应超过 20MB 限制", nil)
	}
	var payload any
	if len(bytes.TrimSpace(raw)) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&payload); err != nil {
			payload = map[string]any{"text": string(raw)}
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := extractMessage(payload)
		return nil, resp.StatusCode, upstreamError(resp.StatusCode, formatUpstreamStatus(resp.StatusCode, message), payload)
	}
	return payload, resp.StatusCode, nil
}

func extractMessage(payload any) string {
	record, ok := asObject(payload)
	if !ok {
		return ""
	}
	for _, key := range []string{"message", "error", "msg"} {
		if text := strings.TrimSpace(stringValue(record[key])); text != "" {
			return text
		}
	}
	return ""
}

func unwrapSub2API(payload any) (any, error) {
	record, ok := asObject(payload)
	if !ok {
		return payload, nil
	}
	code, exists := record["code"]
	if !exists {
		return payload, nil
	}
	if stringValue(code) == "0" {
		return record["data"], nil
	}
	message := extractMessage(record)
	if message == "" {
		message = "sub2api 返回业务错误"
	}
	return nil, &Error{Status: http.StatusBadGateway, Message: message, Details: payload}
}

func unwrapNewAPI(payload any) (any, error) {
	record, ok := asObject(payload)
	if !ok {
		return payload, nil
	}
	if success, exists := record["success"]; exists {
		if value, ok := boolValue(success); ok && !value {
			message := extractMessage(record)
			if message == "" {
				message = "new-api 返回业务错误"
			}
			return nil, &Error{Status: http.StatusBadGateway, Message: message, Details: payload}
		}
		if value, ok := boolValue(success); ok && value {
			if data, exists := record["data"]; exists {
				return data, nil
			}
		}
	}
	return payload, nil
}

func (m *Manager) syncLocked(ctx context.Context, channelID int64) error {
	channel, err := m.store.UpstreamChannel(channelID)
	if err != nil {
		return channelError(err)
	}
	if channel.Type == store.UpstreamChannelOther {
		return invalid("其它渠道仅用于记录，不支持同步")
	}
	if err := m.store.SetUpstreamChannelStatus(channelID, "syncing", ""); err != nil {
		return err
	}

	var result syncResult
	if channel.Type == store.UpstreamChannelSub2API {
		result, err = m.syncSub2API(ctx, channel)
	} else {
		result, err = m.syncNewAPI(ctx, channel)
	}
	if err != nil {
		_ = m.store.SetUpstreamChannelStatus(channelID, "error", err.Error())
		return err
	}
	if err := m.store.SaveUpstreamCache(channelID, "profile", result.raw["profile"], result.profile); err != nil {
		return err
	}
	if err := m.store.SaveUpstreamCache(channelID, "groups", result.raw["groups"], result.groups); err != nil {
		return err
	}
	if err := m.store.SaveUpstreamCache(channelID, "tokens", result.raw["tokens"], result.tokens); err != nil {
		return err
	}
	if channel.Type == store.UpstreamChannelSub2API {
		if err := m.store.SaveUpstreamCache(channelID, "subscriptions", result.raw["subscriptions"], result.subscriptions); err != nil {
			return err
		}
	}
	if err := m.store.MarkUpstreamChannelSynced(channelID); err != nil {
		return err
	}
	if channel.Type == store.UpstreamChannelSub2API {
		if linker := m.multiplierLinker(); linker != nil {
			extraction := tokenMultiplierLinkCandidates(result.groups, result.tokens, channel.Type)
			if extraction.conflicts > 0 {
				m.store.Log("warn", "upstream_multiplier_link_conflict", nil, nil,
					fmt.Sprintf("上游渠道 #%d 有 %d 个令牌 Key 对应冲突倍率，已跳过", channel.ID, extraction.conflicts),
					map[string]any{"conflicts": extraction.conflicts})
			}
			if err := linker(ctx, channel, extraction.ratios); err != nil {
				m.store.Log("warn", "upstream_multiplier_link_failed", nil, nil,
					fmt.Sprintf("上游渠道 #%d 倍率联动失败，将在下一次同步重试", channel.ID), nil)
			}
		}
	}
	return nil
}

func (m *Manager) balanceLocked(ctx context.Context, channelID int64) (*store.UpstreamBalanceSnapshot, error) {
	channel, err := m.store.UpstreamChannel(channelID)
	if err != nil {
		return nil, channelError(err)
	}
	if channel.Type == store.UpstreamChannelOther {
		return nil, invalid("其它渠道仅用于记录，不支持余额查询")
	}
	var profile any
	var snapshot store.UpstreamBalanceSnapshot
	if channel.Type == store.UpstreamChannelSub2API {
		profile, snapshot, err = m.querySub2APIBalance(ctx, channel)
	} else {
		profile, snapshot, _, err = m.queryNewAPIBalance(ctx, channel)
	}
	if err != nil {
		_ = m.store.AddUpstreamBalanceQueryLog(store.UpstreamBalanceQueryLog{
			ChannelID: channelID, Status: "error", Message: "余额查询失败", Error: err.Error(), Raw: errorDetails(err),
		})
		return nil, err
	}
	if _, err := m.store.AddUpstreamBalanceSnapshot(snapshot); err != nil {
		return nil, err
	}
	if err := m.store.AddUpstreamBalanceQueryLog(store.UpstreamBalanceQueryLog{
		ChannelID: channelID, Status: "success", Balance: &snapshot.Balance, UsedBalance: snapshot.UsedBalance,
		Unit: snapshot.Unit, Message: "余额查询成功", Raw: snapshot.Raw,
	}); err != nil {
		return nil, err
	}
	_ = m.store.SaveUpstreamCache(channelID, "profile", profile, profile)
	latest, err := m.store.LatestUpstreamBalanceSnapshot(channelID)
	return latest, err
}

func errorDetails(err error) any {
	var target *Error
	if errors.As(err, &target) {
		return target.Details
	}
	return nil
}

func (m *Manager) syncSub2API(ctx context.Context, channel store.UpstreamChannel) (syncResult, error) {
	if _, err := m.balanceLocked(ctx, channel.ID); err != nil {
		return syncResult{}, err
	}
	profileEntry, err := m.store.UpstreamCache(channel.ID, "profile")
	if err != nil {
		return syncResult{}, err
	}
	profile := profileEntry.Value
	groupsPayload, err := m.sub2APIRequest(ctx, channel, "/groups/available", http.MethodGet, nil, true)
	if err != nil {
		return syncResult{}, err
	}
	rates, err := m.sub2APIRequest(ctx, channel, "/groups/rates", http.MethodGet, nil, true)
	if err != nil {
		rates = map[string]any{}
	}
	groups := applyGroupRates(normalizeCollection(groupsPayload), rates)
	tokens, tokenRaw, err := m.fetchSub2APITokens(ctx, channel)
	if err != nil {
		return syncResult{}, err
	}
	subscriptions := map[string]any{}
	if value, requestErr := m.sub2APIRequest(ctx, channel, "/subscriptions/active", http.MethodGet, nil, true); requestErr != nil {
		subscriptions["active"] = map[string]any{"error": requestErr.Error()}
	} else {
		subscriptions["active"] = value
	}
	if value, requestErr := m.sub2APIRequest(ctx, channel, "/subscriptions/summary", http.MethodGet, nil, true); requestErr != nil {
		subscriptions["summary"] = map[string]any{"error": requestErr.Error()}
	} else {
		subscriptions["summary"] = value
	}
	return syncResult{
		profile: profile, groups: groups, tokens: tokens, subscriptions: subscriptions,
		raw: map[string]any{"profile": profile, "groups": groupsPayload, "tokens": tokenRaw, "subscriptions": subscriptions},
	}, nil
}

func (m *Manager) querySub2APIBalance(ctx context.Context, channel store.UpstreamChannel) (any, store.UpstreamBalanceSnapshot, error) {
	profile, err := m.sub2APIRequest(ctx, channel, "/auth/me", http.MethodGet, nil, true)
	if err != nil {
		return nil, store.UpstreamBalanceSnapshot{}, err
	}
	record, _ := asObject(profile)
	balance, ok := finiteNumber(record["balance"])
	if !ok {
		return nil, store.UpstreamBalanceSnapshot{}, &Error{Status: http.StatusBadGateway, Message: "余额查询失败：profile.balance 缺失或不是有效数字", Details: profile}
	}
	return profile, store.UpstreamBalanceSnapshot{
		ChannelID: channel.ID, Balance: balance, Unit: "sub2api-balance", Raw: profile,
	}, nil
}

func (m *Manager) sub2APILogin(ctx context.Context, channel store.UpstreamChannel) (store.UpstreamChannel, error) {
	if strings.TrimSpace(channel.Username) == "" || channel.Password == "" {
		return store.UpstreamChannel{}, invalid("sub2api 渠道需要账号和密码")
	}
	payload, _, err := m.requestJSON(ctx, http.MethodPost, sub2APIURL(channel, "/auth/login"), map[string]any{
		"email": channel.Username, "password": channel.Password,
	}, nil)
	if err != nil {
		return store.UpstreamChannel{}, err
	}
	data, err := unwrapSub2API(payload)
	if err != nil {
		return store.UpstreamChannel{}, err
	}
	record, ok := asObject(data)
	if !ok {
		return store.UpstreamChannel{}, &Error{Status: http.StatusBadGateway, Message: "sub2api 登录响应格式异常", Details: data}
	}
	if truthy(record["requires_2fa"]) {
		return store.UpstreamChannel{}, invalid("该 sub2api 账号启用了 2FA，当前不支持交互式验证码登录")
	}
	if truthy(record["turnstile_required"]) || truthy(record["requires_turnstile"]) {
		return store.UpstreamChannel{}, invalid("该 sub2api 站点需要 Turnstile 验证，当前不支持交互式验证码登录")
	}
	accessToken := strings.TrimSpace(stringValue(record["access_token"]))
	if accessToken == "" {
		return store.UpstreamChannel{}, &Error{Status: http.StatusBadGateway, Message: "sub2api 登录成功但未返回 access_token", Details: data}
	}
	refreshToken := strings.TrimSpace(stringValue(record["refresh_token"]))
	expiresAt := expiresAtString(record["expires_in"])
	if err := m.store.SaveUpstreamChannelSession(channel.ID, accessToken, refreshToken, expiresAt); err != nil {
		return store.UpstreamChannel{}, err
	}
	return m.store.UpstreamChannel(channel.ID)
}

func (m *Manager) sub2APIRefresh(ctx context.Context, channel store.UpstreamChannel) (store.UpstreamChannel, error) {
	if channel.Sub2APIRefreshToken == "" {
		return m.sub2APILogin(ctx, channel)
	}
	payload, _, err := m.requestJSON(ctx, http.MethodPost, sub2APIURL(channel, "/auth/refresh"), map[string]any{
		"refresh_token": channel.Sub2APIRefreshToken,
	}, nil)
	if err != nil {
		return store.UpstreamChannel{}, err
	}
	data, err := unwrapSub2API(payload)
	if err != nil {
		return store.UpstreamChannel{}, err
	}
	record, _ := asObject(data)
	accessToken := strings.TrimSpace(stringValue(record["access_token"]))
	if accessToken == "" {
		return m.sub2APILogin(ctx, channel)
	}
	refreshToken := strings.TrimSpace(stringValue(record["refresh_token"]))
	if refreshToken == "" {
		refreshToken = channel.Sub2APIRefreshToken
	}
	if err := m.store.SaveUpstreamChannelSession(channel.ID, accessToken, refreshToken, expiresAtString(record["expires_in"])); err != nil {
		return store.UpstreamChannel{}, err
	}
	return m.store.UpstreamChannel(channel.ID)
}

func expiresAtString(value any) string {
	seconds, ok := finiteNumber(value)
	if !ok || seconds <= 0 {
		return ""
	}
	return time.Now().Add(time.Duration(seconds) * time.Second).Format(time.RFC3339Nano)
}

func usableSub2APIAccess(channel store.UpstreamChannel) bool {
	if channel.Sub2APIAccessToken == "" {
		return false
	}
	if channel.Sub2APITokenExpiresAt == "" {
		return true
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, channel.Sub2APITokenExpiresAt)
	if err != nil {
		return false
	}
	return expiresAt.After(time.Now().Add(time.Minute))
}

func (m *Manager) ensureSub2APIAccess(ctx context.Context, channel store.UpstreamChannel, force bool) (store.UpstreamChannel, error) {
	if !force && usableSub2APIAccess(channel) {
		return channel, nil
	}
	if channel.Sub2APIRefreshToken != "" {
		refreshed, err := m.sub2APIRefresh(ctx, channel)
		if err == nil {
			return refreshed, nil
		}
	}
	return m.sub2APILogin(ctx, channel)
}

func (m *Manager) sub2APIRequest(ctx context.Context, channel store.UpstreamChannel, path, method string, body any, retry bool) (any, error) {
	authed, err := m.ensureSub2APIAccess(ctx, channel, false)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+authed.Sub2APIAccessToken)
	payload, _, err := m.requestJSON(ctx, method, sub2APIURL(authed, path), body, headers)
	if err != nil && retry && isUpstreamStatus(err, http.StatusUnauthorized) {
		refreshed, refreshErr := m.ensureSub2APIAccess(ctx, authed, true)
		if refreshErr != nil {
			return nil, refreshErr
		}
		return m.sub2APIRequest(ctx, refreshed, path, method, body, false)
	}
	if err != nil {
		return nil, err
	}
	return unwrapSub2API(payload)
}

func (m *Manager) fetchSub2APITokens(ctx context.Context, channel store.UpstreamChannel) ([]any, []any, error) {
	var lastErr error
	for _, path := range []string{"/keys", "/api-keys"} {
		all := make([]any, 0)
		rawPages := make([]any, 0)
		failed := false
		for page := 1; page <= 50; page++ {
			payload, err := m.sub2APIRequest(ctx, channel, fmt.Sprintf("%s?page=%d&page_size=100", path, page), http.MethodGet, nil, true)
			if err != nil {
				lastErr = err
				failed = true
				break
			}
			rawPages = append(rawPages, payload)
			items, total := extractPage(payload)
			all = append(all, items...)
			if len(items) < 100 || (total >= 0 && len(all) >= total) {
				break
			}
		}
		if !failed {
			return all, rawPages, nil
		}
		if !isUpstreamStatus(lastErr, http.StatusNotFound) {
			return nil, nil, lastErr
		}
	}
	if lastErr != nil {
		return nil, nil, lastErr
	}
	return nil, nil, &Error{Status: http.StatusBadGateway, Message: "无法读取 sub2api 令牌列表"}
}

func applyGroupRates(groups []any, rates any) []any {
	rateMap, ok := asObject(rates)
	if !ok {
		return groups
	}
	for index, group := range groups {
		record, ok := asObject(group)
		if !ok {
			continue
		}
		id := first(record, "id", "ID", "group_id")
		if rate, ok := finiteNumber(rateMap[stringValue(id)]); ok {
			copy := cloneObject(record)
			copy["user_rate_multiplier"] = rate
			groups[index] = copy
		}
	}
	return groups
}

func (m *Manager) syncNewAPI(ctx context.Context, channel store.UpstreamChannel) (syncResult, error) {
	if _, err := m.balanceLocked(ctx, channel.ID); err != nil {
		return syncResult{}, err
	}
	profileEntry, err := m.store.UpstreamCache(channel.ID, "profile")
	if err != nil {
		return syncResult{}, err
	}
	profile := profileEntry.Value
	groupsPayload, err := m.newAPIRequest(ctx, channel, "/user/self/groups", http.MethodGet, nil)
	if err != nil {
		return syncResult{}, err
	}
	tokens, rawPages, err := m.fetchNewAPITokens(ctx, channel)
	if err != nil {
		return syncResult{}, err
	}
	return syncResult{
		profile: profile, groups: normalizeCollection(groupsPayload), tokens: tokens,
		raw: map[string]any{"profile": profile, "groups": groupsPayload, "tokens": rawPages},
	}, nil
}

func (m *Manager) queryNewAPIBalance(ctx context.Context, channel store.UpstreamChannel) (any, store.UpstreamBalanceSnapshot, any, error) {
	profile, err := m.newAPIRequest(ctx, channel, "/user/self", http.MethodGet, nil)
	if err != nil {
		return nil, store.UpstreamBalanceSnapshot{}, nil, err
	}
	record, ok := asObject(profile)
	if !ok {
		return nil, store.UpstreamBalanceSnapshot{}, nil, &Error{Status: http.StatusBadGateway, Message: "new-api 账户响应格式异常", Details: profile}
	}
	if value := first(record, "id", "user_id"); value != nil && stringValue(value) != channel.NewAPIUserID {
		return nil, store.UpstreamBalanceSnapshot{}, nil, invalid("new-api userId 与访问令牌所属用户不一致")
	}
	quota, ok := finiteNumber(record["quota"])
	if !ok {
		return nil, store.UpstreamBalanceSnapshot{}, nil, &Error{Status: http.StatusBadGateway, Message: "余额查询失败：profile.quota 缺失或不是有效数字", Details: profile}
	}
	status, statusErr := m.newAPIRequest(ctx, channel, "/status", http.MethodGet, nil)
	if statusErr != nil {
		status = nil
	}
	conversion := newAPIQuotaConversion(status)
	balance := convertQuota(quota, conversion)
	var used *float64
	if value, ok := finiteNumber(record["used_quota"]); ok {
		converted := convertQuota(value, conversion)
		used = &converted
	}
	snapshot := store.UpstreamBalanceSnapshot{
		ChannelID: channel.ID, Balance: balance, UsedBalance: used, Unit: "new-api-" + conversion.Unit,
		Raw: map[string]any{"profile": profile, "status": status, "conversion": conversion},
	}
	return profile, snapshot, status, nil
}

func newAPIQuotaConversion(status any) quotaConversion {
	record, _ := asObject(status)
	display := strings.ToUpper(strings.TrimSpace(stringValue(first(record, "quota_display_type", "quotaDisplayType"))))
	if display != "CNY" && display != "TOKENS" && display != "CUSTOM" {
		display = "USD"
	}
	quotaPerUnit, ok := finiteNumber(first(record, "quota_per_unit", "quotaPerUnit"))
	if !ok || quotaPerUnit <= 0 {
		quotaPerUnit = defaultNewAPIQuotaPerUnit
	}
	rate := 1.0
	if display == "CNY" {
		if value, ok := finiteNumber(first(record, "usd_exchange_rate", "usdExchangeRate")); ok && value > 0 {
			rate = value
		}
	}
	if display == "CUSTOM" {
		if value, ok := finiteNumber(first(record, "custom_currency_exchange_rate", "customCurrencyExchangeRate")); ok && value > 0 {
			rate = value
		}
	}
	unit := display
	if display == "TOKENS" {
		unit = "tokens"
	}
	return quotaConversion{DisplayType: display, QuotaPerUnit: quotaPerUnit, Rate: rate, Unit: unit}
}

func convertQuota(value float64, conversion quotaConversion) float64 {
	if conversion.DisplayType == "TOKENS" {
		return value
	}
	return value / conversion.QuotaPerUnit * conversion.Rate
}

func (m *Manager) newAPIRequest(ctx context.Context, channel store.UpstreamChannel, path, method string, body any) (any, error) {
	if channel.NewAPIAccessToken == "" || channel.NewAPIUserID == "" {
		return nil, invalid("new-api 渠道需要系统访问令牌和 userId")
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+channel.NewAPIAccessToken)
	headers.Set("New-Api-User", channel.NewAPIUserID)
	payload, _, err := m.requestJSON(ctx, method, newAPIURL(channel, path), body, headers)
	if err != nil {
		return nil, err
	}
	return unwrapNewAPI(payload)
}

func (m *Manager) fetchNewAPITokens(ctx context.Context, channel store.UpstreamChannel) ([]any, []any, error) {
	all := make([]any, 0)
	rawPages := make([]any, 0)
	for page := 1; page <= 50; page++ {
		payload, err := m.newAPIRequest(ctx, channel, fmt.Sprintf("/token/?p=%d&size=100", page), http.MethodGet, nil)
		if err != nil {
			return nil, nil, err
		}
		rawPages = append(rawPages, payload)
		items, total := extractPage(payload)
		all = append(all, items...)
		if len(items) < 100 || (total >= 0 && len(all) >= total) {
			break
		}
	}
	return all, rawPages, nil
}

func (m *Manager) LoginURL(ctx context.Context, channelID int64) (string, error) {
	var result string
	err := m.withChannelLock(ctx, channelID, func() error {
		channel, err := m.store.UpstreamChannel(channelID)
		if err != nil {
			return channelError(err)
		}
		if channel.Type != store.UpstreamChannelSub2API {
			return invalid("当前仅支持 sub2api 渠道自动登录")
		}
		authed, err := m.ensureSub2APIAccess(ctx, channel, true)
		if err != nil {
			return err
		}
		values := make(url.Values)
		values.Set("access_token", authed.Sub2APIAccessToken)
		values.Set("token_type", "Bearer")
		values.Set("redirect", "/dashboard")
		if authed.Sub2APIRefreshToken != "" {
			values.Set("refresh_token", authed.Sub2APIRefreshToken)
		}
		if expiresAt, err := time.Parse(time.RFC3339Nano, authed.Sub2APITokenExpiresAt); err == nil {
			seconds := max(1, int(time.Until(expiresAt).Seconds()))
			values.Set("expires_in", strconv.Itoa(seconds))
		}
		result = strings.TrimRight(authed.BaseURL, "/") + "/auth/oauth/callback#" + values.Encode()
		return nil
	})
	return result, err
}

func (m *Manager) updateTokenGroupLocked(ctx context.Context, channelID, tokenID int64, payload any) (any, error) {
	channel, err := m.store.UpstreamChannel(channelID)
	if err != nil {
		return nil, channelError(err)
	}
	if channel.Type == store.UpstreamChannelOther {
		return nil, invalid("其它渠道仅用于记录，不支持修改令牌分组")
	}
	if tokenID <= 0 {
		return nil, invalid("无效的令牌 ID")
	}
	var updated any
	if channel.Type == store.UpstreamChannelSub2API {
		groupID, ok := positiveInt(firstObjectValue(payload, "group_id", "groupId"))
		if !ok {
			return nil, invalid("sub2api 分组 ID 无效")
		}
		path, token, err := m.sub2APITokenDetail(ctx, channel, tokenID)
		if err != nil {
			return nil, err
		}
		updated, err = m.sub2APIRequest(ctx, channel, fmt.Sprintf("%s/%d", path, tokenID), http.MethodPut, map[string]any{
			"group_id":     groupID,
			"ip_whitelist": stringSlice(token["ip_whitelist"]),
			"ip_blacklist": stringSlice(token["ip_blacklist"]),
		}, true)
		if err != nil {
			return nil, err
		}
		if updated == nil {
			token["group_id"] = groupID
			updated = token
		}
	} else {
		group := stringValue(firstObjectValue(payload, "group"))
		tokenValue, err := m.newAPIRequest(ctx, channel, fmt.Sprintf("/token/%d", tokenID), http.MethodGet, nil)
		if err != nil {
			return nil, err
		}
		token, ok := asObject(tokenValue)
		if !ok {
			return nil, &Error{Status: http.StatusBadGateway, Message: "new-api 令牌详情格式异常", Details: tokenValue}
		}
		body := map[string]any{"id": tokenID, "group": group}
		for _, key := range []string{"name", "expired_time", "remain_quota", "unlimited_quota", "model_limits_enabled", "model_limits", "allow_ips", "cross_group_retry"} {
			value, exists := token[key]
			if !exists {
				return nil, &Error{Status: http.StatusBadGateway, Message: "new-api 令牌详情缺少 " + key + "，无法安全更新分组", Details: tokenValue}
			}
			body[key] = value
		}
		updated, err = m.newAPIRequest(ctx, channel, "/token/", http.MethodPut, body)
		if err != nil {
			return nil, err
		}
		if updated == nil {
			token["group"] = group
			updated = token
		}
	}

	cached := m.updateCachedToken(channelID, updated)
	latest := cached
	if channel.Type == store.UpstreamChannelSub2API {
		if tokens, raw, refreshErr := m.fetchSub2APITokens(ctx, channel); refreshErr == nil {
			latest = tokens
			_ = m.store.SaveUpstreamCache(channelID, "tokens", raw, tokens)
		}
	} else if tokens, raw, refreshErr := m.fetchNewAPITokens(ctx, channel); refreshErr == nil {
		latest = tokens
		_ = m.store.SaveUpstreamCache(channelID, "tokens", raw, tokens)
	}
	return map[string]any{"token": updated, "tokens": latest}, nil
}

func (m *Manager) updateCachedToken(channelID int64, updated any) []any {
	entry, err := m.store.UpstreamCache(channelID, "tokens")
	if err != nil {
		return []any{updated}
	}
	current := normalizeCollection(entry.Value)
	updatedID, ok := tokenID(updated)
	if !ok {
		return current
	}
	found := false
	for index, item := range current {
		if id, ok := tokenID(item); ok && id == updatedID {
			current[index] = updated
			found = true
		}
	}
	if !found {
		current = append([]any{updated}, current...)
	}
	_ = m.store.SaveUpstreamCache(channelID, "tokens", current, current)
	return current
}

func (m *Manager) sub2APITokenDetail(ctx context.Context, channel store.UpstreamChannel, tokenID int64) (string, map[string]any, error) {
	var lastErr error
	for _, path := range []string{"/keys", "/api-keys"} {
		payload, err := m.sub2APIRequest(ctx, channel, fmt.Sprintf("%s/%d", path, tokenID), http.MethodGet, nil, true)
		if err != nil {
			lastErr = err
			if isUpstreamStatus(err, http.StatusNotFound) {
				continue
			}
			return "", nil, err
		}
		token, ok := asObject(payload)
		if !ok {
			return "", nil, &Error{Status: http.StatusBadGateway, Message: "sub2api 令牌详情格式异常", Details: payload}
		}
		return path, token, nil
	}
	if lastErr != nil {
		return "", nil, lastErr
	}
	return "", nil, &Error{Status: http.StatusBadGateway, Message: "无法读取 sub2api 令牌详情"}
}

func (m *Manager) tokenModelsLocked(ctx context.Context, channelID, tokenID int64) (TokenModelsResult, error) {
	channel, err := m.store.UpstreamChannel(channelID)
	if err != nil {
		return TokenModelsResult{}, channelError(err)
	}
	if channel.Type == store.UpstreamChannelOther {
		return TokenModelsResult{}, invalid("其它渠道仅用于记录，不支持查询令牌模型")
	}
	if tokenID <= 0 {
		return TokenModelsResult{}, invalid("无效的令牌 ID")
	}
	if channel.Type == store.UpstreamChannelSub2API {
		_, token, detailErr := m.sub2APITokenDetail(ctx, channel, tokenID)
		if detailErr != nil {
			if cached := m.cachedToken(channelID, tokenID); cached != nil {
				token = cached
			} else {
				return TokenModelsResult{}, detailErr
			}
		}
		key := tokenKey(token)
		if key == "" {
			return TokenModelsResult{}, &Error{Status: http.StatusBadGateway, Message: "sub2api 令牌详情未返回可用密钥，无法查询模型列表", Details: token}
		}
		models, err := m.fetchModelsWithBearer(ctx, channel.BaseURL, key, sub2APIModelPaths(token))
		return TokenModelsResult{TokenID: tokenID, TokenName: tokenName(token), Source: "upstream_models", Models: models}, err
	}

	cached := m.cachedToken(channelID, tokenID)
	payload, detailErr := m.newAPIRequest(ctx, channel, fmt.Sprintf("/token/%d", tokenID), http.MethodGet, nil)
	if detailErr != nil && cached == nil {
		return TokenModelsResult{}, detailErr
	}
	token := cloneObject(cached)
	if detail, ok := asObject(payload); ok {
		for key, value := range detail {
			token[key] = value
		}
	}
	if truthy(token["model_limits_enabled"]) {
		return TokenModelsResult{TokenID: tokenID, TokenName: tokenName(token), Source: "token_limits", Models: splitModelList(token["model_limits"])}, nil
	}
	fullKey, keyErr := m.newAPIRequest(ctx, channel, fmt.Sprintf("/token/%d/key", tokenID), http.MethodPost, nil)
	if keyErr != nil && !optionalUpstreamMiss(keyErr) {
		return TokenModelsResult{}, keyErr
	}
	key := tokenKeyValue(fullKey)
	if key == "" {
		key = tokenKey(token)
	}
	if key != "" {
		if !strings.HasPrefix(key, "sk-") {
			key = "sk-" + key
		}
		if models, fetchErr := m.fetchModelsWithBearer(ctx, channel.BaseURL, key, []string{"/v1/models", "/v1beta/models"}); fetchErr == nil {
			return TokenModelsResult{TokenID: tokenID, TokenName: tokenName(token), Source: "upstream_models", Models: models}, nil
		} else if !optionalUpstreamMiss(fetchErr) {
			return TokenModelsResult{}, fetchErr
		}
	}
	models := make([]string, 0)
	for _, path := range []string{"/user/self/models", "/channel/models_enabled", "/channel/models"} {
		value, requestErr := m.newAPIRequest(ctx, channel, path, http.MethodGet, nil)
		if requestErr != nil {
			if optionalUpstreamMiss(requestErr) {
				continue
			}
			return TokenModelsResult{}, requestErr
		}
		models = append(models, extractModelNames(value)...)
	}
	return TokenModelsResult{TokenID: tokenID, TokenName: tokenName(token), Source: "upstream_models", Models: uniqueStrings(models)}, nil
}

func (m *Manager) cachedToken(channelID, tokenID int64) map[string]any {
	entry, err := m.store.UpstreamCache(channelID, "tokens")
	if err != nil || !entry.Exists {
		return nil
	}
	for _, item := range normalizeCollection(entry.Value) {
		if id, ok := tokenIDValue(item); ok && id == tokenID {
			record, _ := asObject(item)
			return cloneObject(record)
		}
	}
	return nil
}

func (m *Manager) fetchModelsWithBearer(ctx context.Context, baseURL, key string, paths []string) ([]string, error) {
	var lastErr error
	for _, path := range paths {
		headers := make(http.Header)
		headers.Set("Authorization", "Bearer "+key)
		payload, _, err := m.requestJSON(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+path, nil, headers)
		if err != nil {
			lastErr = err
			if isUpstreamStatus(err, 400, 401, 403, 404, 405) {
				continue
			}
			return nil, err
		}
		return extractModelNames(payload), nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return []string{}, nil
}

func sub2APIModelPaths(token map[string]any) []string {
	platform := strings.ToLower(stringValue(first(token, "platform", "group_platform", "groupPlatform")))
	if group, ok := asObject(first(token, "group", "Group")); ok && platform == "" {
		platform = strings.ToLower(stringValue(first(group, "platform", "Platform")))
	}
	switch platform {
	case "gemini", "google":
		return []string{"/v1beta/models", "/v1/models"}
	case "antigravity":
		return []string{"/antigravity/models", "/antigravity/v1/models", "/antigravity/v1beta/models", "/v1/models"}
	case "openai", "anthropic", "claude":
		return []string{"/v1/models"}
	default:
		return []string{"/v1/models", "/v1beta/models", "/antigravity/models"}
	}
}

func extractPage(payload any) ([]any, int) {
	if array, ok := payload.([]any); ok {
		return array, len(array)
	}
	record, ok := asObject(payload)
	if !ok {
		return []any{}, 0
	}
	for _, key := range []string{"items", "data", "rows", "list", "tokens", "keys"} {
		if array, ok := record[key].([]any); ok {
			if total, ok := positiveInt(first(record, "total", "count")); ok {
				return array, total
			}
			return array, -1
		}
	}
	return []any{}, 0
}

func normalizeCollection(value any) []any {
	if array, ok := value.([]any); ok {
		return array
	}
	if array, ok := value.([]map[string]any); ok {
		out := make([]any, len(array))
		for index := range array {
			out[index] = array[index]
		}
		return out
	}
	record, ok := asObject(value)
	if !ok {
		return []any{}
	}
	out := make([]any, 0, len(record))
	for name, raw := range record {
		if item, ok := asObject(raw); ok {
			copy := cloneObject(item)
			if _, exists := copy["name"]; !exists {
				copy["name"] = name
			}
			out = append(out, copy)
		} else {
			out = append(out, map[string]any{"name": name, "value": raw})
		}
	}
	return out
}

func asObject(value any) (map[string]any, bool) {
	record, ok := value.(map[string]any)
	return record, ok
}

func cloneObject(value map[string]any) map[string]any {
	copy := make(map[string]any, len(value))
	for key, item := range value {
		copy[key] = item
	}
	return copy
}

func first(record map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := record[key]; ok {
			return value
		}
	}
	return nil
}

func firstObjectValue(value any, keys ...string) any {
	record, _ := asObject(value)
	return first(record, keys...)
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return fmt.Sprint(typed)
	}
}

func finiteNumber(value any) (float64, bool) {
	if value == nil || stringValue(value) == "" {
		return 0, false
	}
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(stringValue(value)), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	}
	return number, !math.IsNaN(number) && !math.IsInf(number, 0)
}

func positiveInt(value any) (int, bool) {
	number, ok := finiteNumber(value)
	if !ok || number <= 0 || math.Trunc(number) != number || number > float64(^uint(0)>>1) {
		return 0, false
	}
	return int(number), true
}

func boolValue(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, err := strconv.ParseBool(typed)
		return parsed, err == nil
	case json.Number:
		return typed.String() != "0", true
	case float64:
		return typed != 0, true
	}
	return false, false
}

func truthy(value any) bool {
	result, ok := boolValue(value)
	return ok && result
}

func stringSlice(value any) []string {
	array, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]string); ok {
			return typed
		}
		return []string{}
	}
	out := make([]string, 0, len(array))
	for _, item := range array {
		if text := strings.TrimSpace(stringValue(item)); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func tokenID(value any) (int64, bool) { return tokenIDValue(value) }

func tokenIDValue(value any) (int64, bool) {
	record, ok := asObject(value)
	if !ok {
		return 0, false
	}
	number, ok := finiteNumber(first(record, "id", "ID"))
	return int64(number), ok && number > 0 && math.Trunc(number) == number
}

func tokenName(token map[string]any) string {
	return strings.TrimSpace(stringValue(first(token, "name", "Name", "title")))
}

func tokenKey(token map[string]any) string {
	for _, key := range []string{"key", "Key", "api_key", "apiKey", "token"} {
		value := strings.TrimSpace(stringValue(token[key]))
		if value != "" && !strings.Contains(value, "*") {
			return value
		}
	}
	return ""
}

func tokenKeyValue(value any) string {
	if record, ok := asObject(value); ok {
		return tokenKey(record)
	}
	text := strings.TrimSpace(stringValue(value))
	if strings.Contains(text, "*") {
		return ""
	}
	return text
}

func splitModelList(value any) []string {
	switch typed := value.(type) {
	case []any:
		out := make([]string, 0)
		for _, item := range typed {
			out = append(out, splitModelList(item)...)
		}
		return uniqueStrings(out)
	case map[string]any:
		out := make([]string, 0, len(typed))
		for model, enabled := range typed {
			if enabled == nil || enabled == false {
				continue
			}
			out = append(out, normalizeModelName(model))
		}
		return uniqueStrings(out)
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return []string{}
		}
		var parsed any
		if json.Unmarshal([]byte(trimmed), &parsed) == nil {
			if models := splitModelList(parsed); len(models) > 0 {
				return models
			}
		}
		return uniqueStrings(strings.FieldsFunc(trimmed, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' ' }))
	default:
		if text := normalizeModelName(stringValue(value)); text != "" {
			return []string{text}
		}
		return []string{}
	}
}

func extractModelNames(payload any) []string {
	if array, ok := payload.([]any); ok {
		out := make([]string, 0)
		for _, item := range array {
			if record, ok := asObject(item); ok {
				out = append(out, stringValue(first(record, "id", "name", "model", "display_name", "displayName")))
			} else {
				out = append(out, stringValue(item))
			}
		}
		return uniqueStrings(out)
	}
	record, ok := asObject(payload)
	if !ok {
		return []string{}
	}
	for _, key := range []string{"data", "models", "items", "list"} {
		if array, ok := record[key].([]any); ok {
			if models := extractModelNames(array); len(models) > 0 {
				return models
			}
		}
	}
	if models, ok := asObject(record["models"]); ok {
		keys := make([]string, 0, len(models))
		for key := range models {
			keys = append(keys, key)
		}
		return uniqueStrings(keys)
	}
	if nested, ok := asObject(record["data"]); ok {
		if models := extractModelNames(nested); len(models) > 0 {
			return models
		}
	}
	return uniqueStrings([]string{stringValue(first(record, "id", "name", "model"))})
}

func normalizeModelName(value string) string {
	text := strings.TrimSpace(value)
	text = strings.TrimPrefix(text, "models/")
	if index := strings.LastIndex(text, "/models/"); index >= 0 {
		text = text[index+len("/models/"):]
	}
	return strings.TrimSpace(text)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeModelName(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func isUpstreamStatus(err error, statuses ...int) bool {
	var target *Error
	if !errors.As(err, &target) {
		return false
	}
	actual := target.UpstreamCode
	if actual == 0 {
		actual = target.Status
	}
	for _, status := range statuses {
		if actual == status {
			return true
		}
	}
	return false
}

func optionalUpstreamMiss(err error) bool {
	return isUpstreamStatus(err, 400, 401, 403, 404, 405, 502)
}
