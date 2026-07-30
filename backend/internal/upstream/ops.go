package upstream

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// RequestDetail 是 sub2api 运维监控里的一条真实请求记录。
type RequestDetail struct {
	Kind       string    `json:"kind"` // success | error
	CreatedAt  time.Time `json:"created_at"`
	RequestID  string    `json:"request_id"`
	Platform   string    `json:"platform,omitempty"`
	Model      string    `json:"model,omitempty"`
	DurationMs *int      `json:"duration_ms,omitempty"`
	StatusCode *int      `json:"status_code,omitempty"`
	Phase      string    `json:"phase,omitempty"`
	Severity   string    `json:"severity,omitempty"`
	Message    string    `json:"message,omitempty"`
	AccountID  *int64    `json:"account_id,omitempty"`
	GroupID    *int64    `json:"group_id,omitempty"`
	Stream     bool      `json:"stream"`
}

// ListAccountRequests 拉取某账号最近的真实请求记录（最新在前）。
//
// sub2api 的运维监控关闭时返回 ErrMonitoringDisabled，调用方应降级为纯探针模式。
func (c *Client) ListAccountRequests(ctx context.Context, accountID int64, lookback time.Duration, limit int) ([]RequestDetail, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if lookback <= 0 {
		lookback = 2 * time.Hour
	}
	end := time.Now()
	start := end.Add(-lookback)

	path := fmt.Sprintf(
		"/api/v1/admin/ops/requests?account_id=%d&kind=all&page=1&page_size=%d&start_time=%s&end_time=%s",
		accountID, limit,
		start.UTC().Format(time.RFC3339),
		end.UTC().Format(time.RFC3339),
	)

	var data page[RequestDetail]
	if err := c.request(ctx, http.MethodGet, path, nil, &data); err != nil {
		if isMonitoringDisabled(err) {
			return nil, ErrMonitoringDisabled
		}
		return nil, err
	}
	return data.Items, nil
}

// MonitoringEnabled 探测 sub2api 的运维监控是否可用。
func (c *Client) MonitoringEnabled(ctx context.Context) (bool, error) {
	path := "/api/v1/admin/ops/requests?kind=all&page=1&page_size=1"
	var data page[RequestDetail]
	if err := c.request(ctx, http.MethodGet, path, nil, &data); err != nil {
		if isMonitoringDisabled(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// isMonitoringDisabled 识别“监控未开启”这一类响应。
//
// sub2api 用 infraerrors.NotFound("OPS_DISABLED", "Ops monitoring is disabled")
// 表达该状态，落到 HTTP 上是 404 + 含 disabled 的消息。
func isMonitoringDisabled(err error) bool {
	status := StatusCodeOf(err)
	if status == 0 {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "ops_disabled"),
		strings.Contains(msg, "monitoring is disabled"),
		strings.Contains(msg, "ops service not available"),
		strings.Contains(msg, "未开启"):
		return true
	}
	return status == http.StatusServiceUnavailable
}
