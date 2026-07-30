package upstream

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"sub2api-guardian/backend/internal/domain"
)

// ListGroups 拉取全部分组（含未启用的）。
func (c *Client) ListGroups(ctx context.Context) ([]domain.Group, error) {
	var groups []domain.Group
	err := c.request(ctx, http.MethodGet, "/api/v1/admin/groups/all?include_inactive=true", nil, &groups)
	return groups, err
}

// ListAccounts 拉取全部账号。accountType 为空表示不限类型。
func (c *Client) ListAccounts(ctx context.Context, accountType string) ([]domain.Account, error) {
	return fetchAllPages[domain.Account](ctx, c, func(pageNum int) string {
		path := fmt.Sprintf("/api/v1/admin/accounts?page=%d&page_size=200&include_scheduler_score=false", pageNum)
		if accountType != "" {
			path += "&type=" + url.QueryEscape(accountType)
		}
		return path
	})
}

// ListAccountsByGroup 拉取某分组下的账号。
func (c *Client) ListAccountsByGroup(ctx context.Context, groupID int64, accountType string) ([]domain.Account, error) {
	return fetchAllPages[domain.Account](ctx, c, func(pageNum int) string {
		path := fmt.Sprintf("/api/v1/admin/accounts?page=%d&page_size=200&include_scheduler_score=false&group=%d",
			pageNum, groupID)
		if accountType != "" {
			path += "&type=" + url.QueryEscape(accountType)
		}
		return path
	})
}

// Account 读取单个账号。
//
// 手动改动单个渠道后用它做定点刷新，避免为了一条记录触发全量目录同步。
func (c *Client) Account(ctx context.Context, accountID int64) (domain.Account, error) {
	var account domain.Account
	err := c.request(ctx, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/accounts/%d", accountID), nil, &account)
	return account, err
}

// UpdateAccount 提交账号字段变更（只传需要改的字段）。
func (c *Client) UpdateAccount(ctx context.Context, accountID int64, payload map[string]any) error {
	return c.request(ctx, http.MethodPut, fmt.Sprintf("/api/v1/admin/accounts/%d", accountID), payload, nil)
}

// DeleteAccount 从 sub2api 删除账号。
//
// 不可逆：账号接口对凭据做了脱敏，Guardian 拿不到 api_key，
// 因此删除后无法由 Guardian 重建。调用方必须先做好确认与留痕。
func (c *Client) DeleteAccount(ctx context.Context, accountID int64) error {
	return c.request(ctx, http.MethodDelete,
		fmt.Sprintf("/api/v1/admin/accounts/%d", accountID), nil, nil)
}

// SetSchedulable 开关账号的可调度状态，这是熔断与恢复的落地手段。
func (c *Client) SetSchedulable(ctx context.Context, accountID int64, schedulable bool) error {
	return c.request(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/accounts/%d/schedulable", accountID),
		map[string]any{"schedulable": schedulable}, nil)
}

// ClearError 清除账号的错误信息。
func (c *Client) ClearError(ctx context.Context, accountID int64) error {
	return c.request(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/accounts/%d/clear-error", accountID), nil, nil)
}

// RecoverState 让 sub2api 复位账号的限流/临时不可调度等运行期状态。
func (c *Client) RecoverState(ctx context.Context, accountID int64) error {
	return c.request(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/accounts/%d/recover-state", accountID), nil, nil)
}

// ClearRateLimit 清除账号限流标记。
func (c *Client) ClearRateLimit(ctx context.Context, accountID int64) error {
	return c.request(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/accounts/%d/clear-rate-limit", accountID), nil, nil)
}

// Models 返回账号当前可用模型列表。
func (c *Client) Models(ctx context.Context, accountID int64) ([]string, error) {
	var raw any
	if err := c.request(ctx, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/accounts/%d/models", accountID), nil, &raw); err != nil {
		return nil, err
	}
	return collectModelIDs(raw), nil
}

// SyncUpstreamModels 从上游同步模型列表并返回结果。
func (c *Client) SyncUpstreamModels(ctx context.Context, accountID int64) ([]string, error) {
	var raw any
	if err := c.request(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/accounts/%d/models/sync-upstream", accountID), nil, &raw); err != nil {
		return nil, err
	}
	return collectModelIDs(raw), nil
}

// collectModelIDs 从任意嵌套结构里提取模型 ID，兼容不同平台的返回格式。
func collectModelIDs(raw any) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}

	var visit func(any)
	visit = func(v any) {
		switch item := v.(type) {
		case []any:
			for _, child := range item {
				visit(child)
			}
		case map[string]any:
			if id, _ := item["id"].(string); id != "" {
				add(id)
				return
			}
			if id, _ := item["name"].(string); id != "" {
				add(id)
				return
			}
			for _, child := range item {
				visit(child)
			}
		case string:
			add(item)
		}
	}
	visit(raw)
	sort.Strings(out)
	return out
}
