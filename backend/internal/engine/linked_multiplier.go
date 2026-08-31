package engine

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

const linkedMultiplierMarker = "【x"

// SyncLinkedMultipliers 将渠道管理中令牌对应的分组倍率同步到渠道池。
// 账号凭据只在本次调用栈中用于匹配，绝不写入 Guardian 数据库或日志。
func (e *Engine) SyncLinkedMultipliers(
	ctx context.Context,
	channel store.UpstreamChannel,
	tokenMultipliers map[string]float64,
) error {
	if channel.Type != store.UpstreamChannelSub2API || len(tokenMultipliers) == 0 {
		return nil
	}
	validTokenMultipliers := make(map[string]float64, len(tokenMultipliers))
	for key, ratio := range tokenMultipliers {
		key = strings.TrimSpace(key)
		if key == "" || strings.Contains(key, "*") || !validLinkedMultiplier(ratio) {
			continue
		}
		validTokenMultipliers[key] = ratio
	}
	if len(validTokenMultipliers) == 0 {
		return nil
	}
	sourceURL, ok := normalizeLinkedURL(channel.BaseURL)
	if !ok {
		// 上游渠道同步本身已经成功；地址无法规范化时仅跳过联动，
		// 不让一次配置失配改变渠道管理同步的成功状态。
		return nil
	}

	// 同一上游同步不能并发修改联动策略或账号名称。
	e.linkedMultiplierMu.Lock()
	defer e.linkedMultiplierMu.Unlock()

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

	// Export 接口一次只能可靠地关联一个账号 ID，因此用有界并发读取，
	// 避免渠道较多时每次分组同步都串行等待几十个请求。
	type credentialResult struct {
		account     domain.Account
		credentials upstream.AccountCredentials
		err         error
	}
	jobs := make(chan domain.Account)
	results := make(chan credentialResult, len(accountCandidates))
	workerCount := upstreamMultiplierConcurrency
	if workerCount > len(accountCandidates) {
		workerCount = len(accountCandidates)
	}
	var workers sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for account := range jobs {
				credentials, fetchErr := e.client.ExportAccountCredentials(ctx, account.ID)
				select {
				case results <- credentialResult{account: account, credentials: credentials, err: fetchErr}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	for _, account := range accountCandidates {
		select {
		case jobs <- account:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			close(results)
			return ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	close(results)

	matched := make([]linkedAccount, 0)
	for result := range results {
		if result.err != nil {
			// 凭据缺失/类型不符属于正常失配；网络或服务端错误继续记录，
			// 但不让一个账号阻塞其他账号的联动。
			if !isExpectedCredentialMismatch(result.err) {
				accountID := result.account.ID
				e.store.Log("warn", "upstream_multiplier_link_account_failed", &accountID, nil,
					fmt.Sprintf("读取渠道连接信息失败: %s", result.err), nil)
			}
			continue
		}
		accountURL, valid := normalizeLinkedURL(result.credentials.BaseURL)
		if !valid || accountURL != sourceURL {
			continue
		}
		ratio, found := validTokenMultipliers[linkedCredentialKey(result.credentials.APIKey)]
		if !found {
			continue
		}
		matched = append(matched, linkedAccount{account: result.account, ratio: ratio})
	}
	if len(matched) == 0 {
		return nil
	}

	linkedValues := make(map[string]float64, len(matched))
	for _, item := range matched {
		key := itoa(item.account.ID)
		linkedValues[key] = item.ratio
	}
	policyChanged, err := e.store.MergeAccountLinkedMultipliers(linkedValues)
	if err != nil {
		return err
	}

	changed := policyChanged
	for _, item := range matched {
		desiredName := linkedMultiplierName(item.account.Name, item.ratio)
		if desiredName == item.account.Name {
			continue
		}
		accountID := item.account.ID
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
		changed = true
		e.store.Log("info", "upstream_multiplier_linked", &accountID, nil,
			fmt.Sprintf("渠道管理倍率已同步为 %g，名称后缀已更新", item.ratio), map[string]any{
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
	if changed {
		e.fireNotify()
	}
	return nil
}

type linkedAccount struct {
	account domain.Account
	ratio   float64
}

func linkedCredentialKey(apiKey string) string {
	return strings.TrimSpace(apiKey)
}

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
	return strings.Contains(message, "未配置 API Key") || strings.Contains(message, "未配置上游地址") ||
		strings.Contains(message, "只有 API Key 类型")
}
