package upstream

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	dailyReportPageSize = 1000
	dailyReportMaxPages = 100
)

// DailyReportStats 是每日报告需要的四项统计。
// 充值金额按币种分别保存，避免把不同币种直接相加。
type DailyReportStats struct {
	TotalActualCost float64            `json:"total_actual_cost"`
	TotalTokens     int64              `json:"total_tokens"`
	NewUsers        int                `json:"new_users"`
	RechargeAmounts map[string]float64 `json:"recharge_amounts"`
}

type dailyUsageStats struct {
	TotalActualCost any `json:"total_actual_cost"`
	TotalTokens     any `json:"total_tokens"`
}

type dailyUser struct {
	CreatedAt any `json:"created_at"`
}

type dailyPaymentOrder struct {
	Status    string `json:"status"`
	OrderType string `json:"order_type"`
	Amount    any    `json:"amount"`
	PayAmount any    `json:"pay_amount"`
	Currency  string `json:"currency"`
	PaidAt    any    `json:"paid_at"`
	CreatedAt any    `json:"created_at"`
}

// GetDailyReportStats 读取指定时区当天截至 end 的统计数据。
func (c *Client) GetDailyReportStats(ctx context.Context, start, end time.Time, timezone string) (DailyReportStats, error) {
	if err := c.Ready(); err != nil {
		return DailyReportStats{}, err
	}
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return DailyReportStats{}, fmt.Errorf("每日报告查询时间范围无效")
	}
	location, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil {
		return DailyReportStats{}, fmt.Errorf("每日报告查询时区无效: %w", err)
	}

	query := dailyDateQuery(start, end, location)
	var usage dailyUsageStats
	if err := c.requestDaily(ctx, http.MethodGet, "/api/v1/admin/usage/stats?"+query+"&nocache=true", &usage); err != nil {
		return DailyReportStats{}, fmt.Errorf("查询每日 usage 统计失败: %w", err)
	}
	totalActualCost, ok := anyFloat(usage.TotalActualCost)
	if !ok || totalActualCost < 0 || math.IsNaN(totalActualCost) || math.IsInf(totalActualCost, 0) {
		return DailyReportStats{}, fmt.Errorf("每日 usage 统计缺少有效 total_actual_cost")
	}
	totalTokens, ok := anyInt64(usage.TotalTokens)
	if !ok || totalTokens < 0 {
		return DailyReportStats{}, fmt.Errorf("每日 usage 统计缺少有效 total_tokens")
	}

	newUsers, err := c.countDailyUsers(ctx, start, end, location)
	if err != nil {
		return DailyReportStats{}, err
	}
	rechargeAmounts, err := c.sumDailyRecharge(ctx, start, end, location)
	if err != nil {
		return DailyReportStats{}, err
	}
	return DailyReportStats{
		TotalActualCost: totalActualCost,
		TotalTokens:     totalTokens,
		NewUsers:        newUsers,
		RechargeAmounts: rechargeAmounts,
	}, nil
}

func (c *Client) countDailyUsers(ctx context.Context, start, end time.Time, location *time.Location) (int, error) {
	pathBase := "/api/v1/admin/users?" + url.Values{
		"page_size":  []string{strconv.Itoa(dailyReportPageSize)},
		"sort_by":    []string{"created_at"},
		"sort_order": []string{"desc"},
	}.Encode()
	count := 0
	for pageNumber := 1; pageNumber <= dailyReportMaxPages; pageNumber++ {
		var data page[dailyUser]
		path := pathBase + "&page=" + strconv.Itoa(pageNumber)
		if err := c.requestDaily(ctx, http.MethodGet, path, &data); err != nil {
			return 0, fmt.Errorf("查询每日注册用户失败: %w", err)
		}
		stop := false
		for _, item := range data.Items {
			createdAt, ok := ParseUsageTime(item.CreatedAt, location)
			if !ok {
				continue
			}
			if createdAt.Before(start) {
				stop = true
				break
			}
			if createdAt.Before(end) {
				count++
			}
		}
		if stop || len(data.Items) == 0 || len(data.Items) < dailyReportPageSize ||
			(data.Pages > 0 && pageNumber >= data.Pages) {
			break
		}
	}
	return count, nil
}

func (c *Client) sumDailyRecharge(ctx context.Context, start, end time.Time, location *time.Location) (map[string]float64, error) {
	pathBase := "/api/v1/admin/payment/orders?" + url.Values{
		"order_type": []string{"balance"},
		"page_size":  []string{strconv.Itoa(dailyReportPageSize)},
	}.Encode()
	amounts := make(map[string]float64)
	for pageNumber := 1; pageNumber <= dailyReportMaxPages; pageNumber++ {
		var data page[dailyPaymentOrder]
		path := pathBase + "&page=" + strconv.Itoa(pageNumber)
		if err := c.requestDaily(ctx, http.MethodGet, path, &data); err != nil {
			return nil, fmt.Errorf("查询每日充值订单失败: %w", err)
		}
		stop := false
		for _, order := range data.Items {
			createdAt, createdOK := ParseUsageTime(order.CreatedAt, location)
			if createdOK && createdAt.Before(start) {
				stop = true
				break
			}
			if !dailyRechargeStatus(order.Status) {
				continue
			}
			paidAt, ok := ParseUsageTime(order.PaidAt, location)
			if !ok || paidAt.Before(start) || !paidAt.Before(end) {
				continue
			}
			amount, ok := anyFloat(order.PayAmount)
			if !ok {
				amount, ok = anyFloat(order.Amount)
			}
			if !ok || amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
				continue
			}
			currency := strings.ToUpper(strings.TrimSpace(order.Currency))
			if currency == "" {
				currency = "CNY"
			}
			amounts[currency] += amount
		}
		if stop || len(data.Items) == 0 || len(data.Items) < dailyReportPageSize ||
			(data.Pages > 0 && pageNumber >= data.Pages) {
			break
		}
	}
	for currency, amount := range amounts {
		amounts[currency] = math.Round(amount*100) / 100
	}
	return amounts, nil
}

func dailyRechargeStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "PAID", "RECHARGING", "COMPLETED":
		return true
	default:
		return false
	}
}

func dailyDateQuery(start, end time.Time, location *time.Location) string {
	return url.Values{
		"start_date": []string{start.In(location).Format("2006-01-02")},
		"end_date":   []string{end.In(location).Format("2006-01-02")},
		"timezone":   []string{location.String()},
	}.Encode()
}

func (c *Client) requestDaily(ctx context.Context, method, path string, out any) error {
	var lastErr error
	for attempt := 0; attempt < usageMaxAttempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, usageAttemptTime)
		err := c.request(attemptCtx, method, path, nil, out)
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

func anyInt64(raw any) (int64, bool) {
	value, ok := anyFloat(raw)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value > math.MaxInt64 || value < math.MinInt64 {
		return 0, false
	}
	return int64(value), true
}
