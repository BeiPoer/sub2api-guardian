package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	usagePageSize    = 100
	usageMaxAttempts = 4
	usageAttemptTime = 45 * time.Second
)

// UsageRecord 是 /api/v1/admin/usage 返回的一条记录。
//
// usage 接口在不同版本中会把 ID、首 T 和嵌套对象的字段返回成不同 JSON 类型，
// 因此这里对这些字段保留原始值，由报告层按兼容规则解释；其余字段不进入内存。
type UsageRecord struct {
	CreatedAt    any `json:"created_at"`
	FirstTokenMS any `json:"first_token_ms"`
	Group        any `json:"group"`
	GroupID      any `json:"group_id"`
	Account      any `json:"account"`
	AccountID    any `json:"account_id"`
}

// ListUsage 分页读取统计窗口附近的 usage records。
//
// 接口按 created_at 倒序返回。只要某页出现早于窗口起点的记录，后续页面就不再
// 请求；但当前页仍会完整返回给调用方，由调用方执行最终的闭区间过滤。
func (c *Client) ListUsage(ctx context.Context, start, end time.Time, timezone string) ([]UsageRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, usageAttemptTime)
	defer cancel()
	if err := c.Ready(); err != nil {
		return nil, err
	}
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return nil, errors.New("usage 查询时间范围无效")
	}
	loc, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil {
		return nil, fmt.Errorf("usage 查询时区无效: %w", err)
	}

	startLocal := start.In(loc)
	endLocal := end.In(loc)
	pathBase := "/api/v1/admin/usage?" + url.Values{
		"start_date":  []string{startLocal.Format("2006-01-02")},
		"end_date":    []string{endLocal.AddDate(0, 0, 1).Format("2006-01-02")},
		"timezone":    []string{loc.String()},
		"exact_total": []string{"false"},
		"page_size":   []string{strconv.Itoa(usagePageSize)},
		"sort_by":     []string{"created_at"},
		"sort_order":  []string{"desc"},
	}.Encode()

	items := make([]UsageRecord, 0, usagePageSize)
	for pageNumber := 1; pageNumber <= 100; pageNumber++ {
		path := pathBase + "&page=" + strconv.Itoa(pageNumber)
		var data page[UsageRecord]
		if err := c.requestUsagePage(ctx, path, &data); err != nil {
			return nil, err
		}

		stop := false
		for _, item := range data.Items {
			items = append(items, item)
			if createdAt, ok := ParseUsageTime(item.CreatedAt, loc); ok && createdAt.Before(start) {
				stop = true
			}
		}
		if stop || len(data.Items) == 0 || len(data.Items) < usagePageSize ||
			(data.Pages > 0 && pageNumber >= data.Pages) {
			break
		}
	}
	return items, nil
}

func (c *Client) requestUsagePage(ctx context.Context, path string, out any) error {
	var lastErr error
	for attempt := 0; attempt < usageMaxAttempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, usageAttemptTime)
		err := c.request(attemptCtx, http.MethodGet, path, nil, out)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryableUsageError(err) || attempt == usageMaxAttempts-1 {
			return err
		}
		backoff := time.Duration(1<<attempt) * time.Second
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func retryableUsageError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, ErrNotConfigured) || errors.Is(err, ErrNewAPINotConfigured) {
		return false
	}
	status := StatusCodeOf(err)
	if status != 0 {
		return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// ParseUsageTime 将 usage 时间戳解析为可比较的时间。
// RFC3339Nano 覆盖带 Z 的 ISO 8601 时间，也兼容上游常见的秒/毫秒 Unix 时间。
func ParseUsageTime(value any, location *time.Location) (time.Time, bool) {
	switch typed := value.(type) {
	case time.Time:
		return typed, !typed.IsZero()
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, text); err == nil {
				if layout == "2006-01-02 15:04:05" && location != nil {
					return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), parsed.Hour(), parsed.Minute(), parsed.Second(), parsed.Nanosecond(), location), true
				}
				return parsed, true
			}
		}
		return time.Time{}, false
	case json.Number:
		return unixUsageTime(typed.String())
	case float64:
		return unixUsageTime(strconv.FormatFloat(typed, 'f', -1, 64))
	case float32:
		return unixUsageTime(strconv.FormatFloat(float64(typed), 'f', -1, 64))
	case int:
		return time.Unix(int64(typed), 0), true
	case int64:
		return time.Unix(typed, 0), true
	default:
		return time.Time{}, false
	}
}

func unixUsageTime(raw string) (time.Time, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return time.Time{}, false
	}
	seconds := value
	if math.Abs(value) >= 1e11 {
		seconds = value / 1000
	}
	whole, fraction := math.Modf(seconds)
	return time.Unix(int64(whole), int64(fraction*1e9)), true
}
