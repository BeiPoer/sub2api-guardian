package engine

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

const linkedMultiplierMarker = "【x"

const (
	linkedCredentialCacheTTL = 10 * time.Minute
)

// SyncLinkedMultipliers 将渠道管理中令牌对应的分组倍率同步到渠道池。
// 账号凭据只在当前进程的短期缓存中用于匹配，绝不写入 Guardian 数据库或日志。
func (e *Engine) SyncLinkedMultipliers(
	ctx context.Context,
	channel store.UpstreamChannel,
	tokenMultipliers map[string]float64,
) error {
	if !supportsLinkedUpstreamType(channel.Type) || len(tokenMultipliers) == 0 {
		return nil
	}
	// 远程倍率源模式下，本地渠道目录仍可用于查看和同步，但不能再覆盖
	// G1 提供的联动值。
	settings, _, err := e.store.MultiplierSourceSettings()
	if err != nil {
		return err
	}
	if settings.Mode == store.MultiplierSourceRemote {
		return nil
	}
	rechargeRatio := channel.RechargeRatio
	if !validLinkedMultiplier(rechargeRatio) {
		rechargeRatio = 1
	}
	validTokenMultipliers := make(map[string]float64, len(tokenMultipliers))
	for key, upstreamRatio := range tokenMultipliers {
		key = strings.TrimSpace(key)
		if channel.Type == store.UpstreamChannelNewAPI && key != "" && !strings.HasPrefix(key, "sk-") {
			key = "sk-" + key
		}
		if key == "" || strings.Contains(key, "*") || !validLinkedMultiplier(upstreamRatio) {
			continue
		}
		// 渠道管理的充值比例是 1:x；上游令牌倍率需换算成 Guardian
		// 调度口径，避免把“充值 1 元到账 x 元”重复当成成本倍率。
		ratio := upstreamRatio / rechargeRatio
		if !validLinkedMultiplier(ratio) {
			continue
		}
		validTokenMultipliers[key] = ratio
	}
	if len(validTokenMultipliers) == 0 {
		return nil
	}
	// 同一上游同步不能并发修改联动策略或账号名称。
	e.linkedMultiplierMu.Lock()
	defer e.linkedMultiplierMu.Unlock()
	// 配置切换可能发生在上面的快速检查之后；锁内再确认一次，避免旧的本地
	// 同步结果覆盖刚切换到远程源的值。
	settings, _, err = e.store.MultiplierSourceSettings()
	if err != nil {
		return err
	}
	if settings.Mode == store.MultiplierSourceRemote {
		return nil
	}

	accounts, err := e.store.Accounts()
	if err != nil {
		return err
	}
	accountCandidates := make([]domain.Account, 0, len(accounts))
	for _, account := range accounts {
		if policy.IsAPIKeyType(account.Type) {
			accountCandidates = append(accountCandidates, account)
		}
	}
	if len(accountCandidates) == 0 {
		return nil
	}
	if err := e.client.Ready(); err != nil {
		return err
	}

	credentialsByID, err := e.linkedCredentials(ctx)
	if err != nil {
		return err
	}

	matched := make([]linkedAccount, 0)
	for _, account := range accountCandidates {
		credentials, found := credentialsByID[account.ID]
		if !found {
			continue
		}
		keys := []string{credentials.APIKey}
		if channel.Type == store.UpstreamChannelNewAPI {
			keys = sourceLinkedKeyVariants(credentials.APIKey)
		}
		var ratio float64
		matchedKey := false
		for _, key := range keys {
			if candidate, ok := validTokenMultipliers[linkedCredentialKey(key)]; ok {
				ratio, matchedKey = candidate, true
				break
			}
		}
		if !matchedKey {
			continue
		}
		matched = append(matched, linkedAccount{account: account, ratio: ratio})
	}
	if len(matched) == 0 {
		return nil
	}
	_, err = e.applyLinkedMultiplierMatches(ctx, matched, "渠道管理")
	return err
}

func supportsLinkedUpstreamType(channelType store.UpstreamChannelType) bool {
	return channelType == store.UpstreamChannelSub2API || channelType == store.UpstreamChannelNewAPI
}

// linkedCredentials 复用短期批量凭据快照，避免每个上游渠道都重复读取
// 全部渠道池账号。凭据不会写入数据库、日志或 API 响应，10 分钟后自动刷新。
func (e *Engine) linkedCredentials(ctx context.Context) (map[int64]upstream.AccountCredentials, error) {
	now := time.Now()
	e.linkedCredentialMu.Lock()
	config := e.linkedCredentialConfig
	if !e.linkedCredentialFetchedAt.IsZero() && now.Sub(e.linkedCredentialFetchedAt) < linkedCredentialCacheTTL {
		cached := make(map[int64]upstream.AccountCredentials, len(e.linkedCredentialCache))
		for accountID, entry := range e.linkedCredentialCache {
			cached[accountID] = entry.credentials
		}
		e.linkedCredentialMu.Unlock()
		return cached, nil
	}
	e.linkedCredentialMu.Unlock()

	started := time.Now()
	records, err := e.client.ListAccountCredentials(ctx)
	if err != nil {
		log.Printf("倍率联动批量读取账号失败: %v", err)
		return nil, err
	}
	log.Printf("倍率联动批量读取账号完成: %d 个，耗时 %s", len(records), time.Since(started).Round(time.Millisecond))
	fresh := make(map[int64]upstream.AccountCredentials, len(records))
	for _, record := range records {
		fresh[record.AccountID] = record.Credentials
	}

	e.linkedCredentialMu.Lock()
	if !e.linkedCredentialReady || e.linkedCredentialConfig == config {
		cache := make(map[int64]linkedCredentialCacheEntry, len(fresh))
		for accountID, credentials := range fresh {
			cache[accountID] = linkedCredentialCacheEntry{
				credentials: credentials,
			}
		}
		e.linkedCredentialCache = cache
		e.linkedCredentialFetchedAt = now
	}
	e.linkedCredentialMu.Unlock()
	return fresh, nil
}

type linkedAccount struct {
	account domain.Account
	ratio   float64
	// fingerprint 仅由远程倍率源使用；本地渠道管理为空。
	fingerprint string
}

// linkedApplyResult 返回名称归属信息，远程源据此安全清理自己生成的后缀。
type linkedApplyResult struct {
	changed       bool
	generatedName map[string]string
}

// applyLinkedMultiplierMatches 统一应用本地与远程倍率源的匹配结果。
// 调用方只需提供 G2 本地账号和最终调度倍率，所有副作用保持同一套行为。
func (e *Engine) applyLinkedMultiplierMatches(
	ctx context.Context,
	matched []linkedAccount,
	sourceLabel string,
) (linkedApplyResult, error) {
	result := linkedApplyResult{generatedName: make(map[string]string, len(matched))}
	if len(matched) == 0 {
		return result, nil
	}
	linkedValues := make(map[string]float64, len(matched))
	for _, item := range matched {
		linkedValues[itoa(item.account.ID)] = item.ratio
	}
	policyChanged, err := e.store.MergeAccountLinkedMultipliers(linkedValues)
	if err != nil {
		return result, err
	}
	result.changed = policyChanged
	for _, item := range matched {
		desiredName := linkedMultiplierName(item.account.Name, item.ratio)
		accountID := item.account.ID
		if desiredName == item.account.Name {
			result.generatedName[itoa(accountID)] = desiredName
			continue
		}
		if err := e.client.UpdateAccount(ctx, accountID, map[string]any{"name": desiredName}); err != nil {
			e.store.Log("warn", "upstream_multiplier_link_name_failed", &accountID, nil,
				"同步渠道名称失败，将在下一次同步重试", nil)
			continue
		}
		// 目录同步可能与联动并行；重新读取当前缓存只替换名称，避免
		// 用联动开始时的旧状态覆盖刚同步的账号字段，也不在账号已删除时复活它。
		cached, cacheErr := e.store.Account(accountID)
		if cacheErr != nil {
			e.store.Log("warn", "upstream_multiplier_link_cache_failed", &accountID, nil,
				fmt.Sprintf("渠道名称已写回，但读取本地缓存失败: %s", cacheErr), nil)
		} else {
			cached.Name = desiredName
			if err := e.store.UpsertAccount(cached); err != nil {
				e.store.Log("warn", "upstream_multiplier_link_cache_failed", &accountID, nil,
					fmt.Sprintf("渠道名称已写回，但刷新本地缓存失败: %s", err), nil)
			}
		}
		result.generatedName[itoa(accountID)] = desiredName
		result.changed = true
		e.store.Log("info", "upstream_multiplier_linked", &accountID, nil,
			fmt.Sprintf("%s倍率已同步为 %g，名称后缀已更新", sourceLabel, item.ratio), map[string]any{
				"multiplier": item.ratio,
				"name":       desiredName,
			})
	}
	if policyChanged {
		if err := e.refreshGroupStates(); err != nil {
			// 联动倍率已经可靠写入策略；聚合刷新留给下一轮修复，
			// 不能因此把上游渠道同步标成失败或阻断其他账号。
			e.store.Log("warn", "upstream_multiplier_link_refresh_failed", nil, nil,
				fmt.Sprintf("联动倍率已保存，但分组聚合刷新失败: %s", err), nil)
		}
	}
	if result.changed {
		e.fireNotify()
	}
	return result, nil
}

type linkedCredentialCacheEntry struct {
	credentials upstream.AccountCredentials
}

func linkedCredentialKey(apiKey string) string {
	return strings.TrimSpace(apiKey)
}

// normalizeLinkedURL 保留旧的 URL 规范化辅助函数；联动匹配本身已不再使用 URL。
func normalizeLinkedURL(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "", false
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if parsed.RawPath != "" {
		parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), true
}

func linkedMultiplierName(name string, ratio float64) string {
	base := strings.TrimSpace(name)
	if index := strings.Index(base, linkedMultiplierMarker); index >= 0 {
		// 保留标记前的原始前缀（包括用户刻意留下的分隔空格），
		// 只替换从第一个「【x」开始的旧后缀。
		base = base[:index]
	}
	return base + linkedMultiplierMarker + formatLinkedMultiplier(ratio) + "】"
}

func formatLinkedMultiplier(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func validLinkedMultiplier(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func isExpectedCredentialMismatch(err error) bool {
	message := err.Error()
	return strings.Contains(message, "未配置 API Key") || strings.Contains(message, "只有 API Key 类型")
}
