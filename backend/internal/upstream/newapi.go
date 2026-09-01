package upstream

import (
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
)

const (
	newAPIPageSize = 100
	newAPIMaxPages = 10_000
)

var ErrNewAPINotConfigured = errors.New("New API 报告源站未配置完整")

type NewAPIClient struct {
	baseURL    string
	credential string
	userID     int64
	http       *http.Client
}

type NewAPIError struct {
	Method     string
	Path       string
	StatusCode int
	Message    string
}

func (e *NewAPIError) Error() string {
	return fmt.Sprintf("New API %s %s 返回 %d: %s", e.Method, e.Path, e.StatusCode, e.Message)
}

type newAPIEnvelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type newAPIPage[T any] struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
	Items    []T `json:"items"`
}

type newAPILog struct {
	CreatedAt        int64  `json:"created_at"`
	Quota            int64  `json:"quota"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	ChannelID        int64  `json:"channel"`
	ChannelName      string `json:"channel_name"`
	Group            string `json:"group"`
	Other            string `json:"other"`
}

type newAPIUser struct {
	CreatedAt int64 `json:"created_at"`
}

type newAPITopUp struct {
	Amount       float64 `json:"amount"`
	UserID       int64   `json:"user_id"`
	CompleteTime int64   `json:"complete_time"`
	Status       string  `json:"status"`
}

type newAPIStatus struct {
	QuotaPerUnit               float64 `json:"quota_per_unit"`
	QuotaDisplayType           string  `json:"quota_display_type"`
	USDExchangeRate            float64 `json:"usd_exchange_rate"`
	CustomCurrencySymbol       string  `json:"custom_currency_symbol"`
	CustomCurrencyExchangeRate float64 `json:"custom_currency_exchange_rate"`
}

type newAPIQuotaDisplay struct {
	typeName           string
	unit               string
	quotaPerUnit       float64
	usdExchangeRate    float64
	customExchangeRate float64
}

func NewNewAPI(baseURL, credential string, userID int64, timeout time.Duration) *NewAPIClient {
	if timeout <= 0 {
		timeout = time.Minute
	}
	return &NewAPIClient{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		credential: strings.TrimSpace(credential), userID: userID,
		http: &http.Client{Timeout: timeout},
	}
}

func (c *NewAPIClient) Ready() error {
	if c.baseURL == "" || c.credential == "" || c.userID <= 0 {
		return ErrNewAPINotConfigured
	}
	return nil
}

func (c *NewAPIClient) BaseURL() string { return c.baseURL }

func (c *NewAPIClient) ListUsage(ctx context.Context, start, end time.Time, timezone string) ([]UsageRecord, error) {
	if _, err := reportLocationAndRange(start, end, timezone); err != nil {
		return nil, err
	}
	logs, err := c.consumptionLogs(ctx, start, end)
	if err != nil {
		return nil, err
	}
	records := make([]UsageRecord, 0, len(logs))
	for _, item := range logs {
		createdAt := time.Unix(item.CreatedAt, 0)
		if createdAt.Before(start) || !createdAt.Before(end) {
			continue
		}
		group := strings.TrimSpace(item.Group)
		if group == "" {
			group = "未知分组"
		}
		account := strings.TrimSpace(item.ChannelName)
		if account == "" {
			account = "channel-" + strconv.FormatInt(item.ChannelID, 10)
		}
		records = append(records, UsageRecord{
			CreatedAt: item.CreatedAt, FirstTokenMS: newAPIFirstTokenMS(item.Other),
			Group: group, Account: account,
		})
	}
	return records, nil
}

func (c *NewAPIClient) GetDailyReportStats(ctx context.Context, start, end time.Time, timezone string) (DailyReportStats, error) {
	location, err := reportLocationAndRange(start, end, timezone)
	if err != nil {
		return DailyReportStats{}, err
	}
	var status newAPIStatus
	if err := c.requestWithRetry(ctx, http.MethodGet, "/api/status", &status); err != nil {
		return DailyReportStats{}, fmt.Errorf("查询 New API 额度配置失败: %w", err)
	}
	display, err := newQuotaDisplay(status)
	if err != nil {
		return DailyReportStats{}, err
	}
	logs, err := c.consumptionLogs(ctx, start, end)
	if err != nil {
		return DailyReportStats{}, fmt.Errorf("查询 New API 每日消费日志失败: %w", err)
	}
	var rawQuota float64
	var totalTokens int64
	for _, item := range logs {
		createdAt := time.Unix(item.CreatedAt, 0)
		if createdAt.Before(start) || !createdAt.Before(end) {
			continue
		}
		rawQuota += float64(item.Quota)
		totalTokens += item.PromptTokens + item.CompletionTokens
	}

	users, err := fetchNewAPIPages[newAPIUser](ctx, c, func(page int) string {
		return "/api/user/?" + url.Values{
			"p": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(newAPIPageSize)},
			"sort_by": {"created_at"}, "sort_order": {"desc"},
		}.Encode()
	})
	if err != nil {
		return DailyReportStats{}, fmt.Errorf("查询 New API 每日注册用户失败: %w", err)
	}
	newUsers := 0
	for _, item := range users {
		createdAt := time.Unix(item.CreatedAt, 0).In(location)
		if !createdAt.Before(start) && createdAt.Before(end) {
			newUsers++
		}
	}

	topUps, err := fetchNewAPIPages[newAPITopUp](ctx, c, func(page int) string {
		return "/api/user/topup?" + url.Values{
			"p": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(newAPIPageSize)},
		}.Encode()
	})
	if err != nil {
		return DailyReportStats{}, fmt.Errorf("查询 New API 每日充值记录失败: %w", err)
	}
	rechargeAmount := 0.0
	rechargeUsers := make(map[int64]struct{})
	for _, item := range topUps {
		completedAt := time.Unix(item.CompleteTime, 0)
		if !strings.EqualFold(strings.TrimSpace(item.Status), "success") || completedAt.Before(start) || !completedAt.Before(end) {
			continue
		}
		if item.Amount > 0 && !math.IsNaN(item.Amount) && !math.IsInf(item.Amount, 0) {
			rechargeAmount += display.fromUSD(item.Amount)
		}
		if item.UserID > 0 {
			rechargeUsers[item.UserID] = struct{}{}
		}
	}
	rechargeAmounts := make(map[string]float64)
	if rechargeAmount != 0 {
		rechargeAmounts[display.unit] = rechargeAmount
	}
	return DailyReportStats{
		TotalActualCost: display.fromQuota(rawQuota), QuotaUnit: display.unit,
		TotalTokens: totalTokens, NewUsers: newUsers,
		RechargeAmounts: rechargeAmounts, RechargeUsers: len(rechargeUsers),
	}, nil
}

func (c *NewAPIClient) consumptionLogs(ctx context.Context, start, end time.Time) ([]newAPILog, error) {
	return fetchNewAPIPages[newAPILog](ctx, c, func(page int) string {
		return "/api/log/?" + url.Values{
			"type": {"2"}, "start_timestamp": {strconv.FormatInt(start.Unix(), 10)},
			"end_timestamp": {strconv.FormatInt(end.Unix(), 10)},
			"p":             {strconv.Itoa(page)}, "page_size": {strconv.Itoa(newAPIPageSize)},
		}.Encode()
	})
}

func fetchNewAPIPages[T any](ctx context.Context, client *NewAPIClient, path func(int) string) ([]T, error) {
	items := make([]T, 0, newAPIPageSize)
	for pageNumber := 1; pageNumber <= newAPIMaxPages; pageNumber++ {
		var page newAPIPage[T]
		if err := client.requestWithRetry(ctx, http.MethodGet, path(pageNumber), &page); err != nil {
			return nil, err
		}
		items = append(items, page.Items...)
		pageSize := page.PageSize
		if pageSize <= 0 {
			pageSize = newAPIPageSize
		}
		if len(page.Items) == 0 || (page.Total > 0 && len(items) >= page.Total) || (page.Total <= 0 && len(page.Items) < pageSize) {
			return items, nil
		}
	}
	return nil, errors.New("New API 分页数量超过安全上限")
}

func (c *NewAPIClient) requestWithRetry(ctx context.Context, method, path string, out any) error {
	var lastErr error
	for attempt := 0; attempt < usageMaxAttempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, usageAttemptTime)
		err := c.request(attemptCtx, method, path, out)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryableUsageError(err) || attempt == usageMaxAttempts-1 {
			return err
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * time.Second)
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

func (c *NewAPIClient) request(ctx context.Context, method, path string, out any) error {
	if err := c.Ready(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.credential)
	req.Header.Set("New-Api-User", strconv.FormatInt(c.userID, 10))
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return err
	}
	var envelope newAPIEnvelope
	_ = json.Unmarshal(raw, &envelope)
	message := c.safeMessage(envelope.Message)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if message == "" {
			message = c.safeMessage(strings.TrimSpace(string(raw)))
		}
		return &NewAPIError{Method: method, Path: path, StatusCode: resp.StatusCode, Message: message}
	}
	if !envelope.Success {
		if message == "" {
			message = "上游返回业务错误"
		}
		return errors.New(message)
	}
	if out == nil {
		return nil
	}
	if envelope.Data == nil {
		return errors.New("New API 响应缺少 data")
	}
	return json.Unmarshal(envelope.Data, out)
}

func (c *NewAPIClient) safeMessage(message string) string {
	message = strings.TrimSpace(message)
	if c.credential != "" {
		message = strings.ReplaceAll(message, c.credential, "[redacted]")
	}
	return message
}

func reportLocationAndRange(start, end time.Time, timezone string) (*time.Location, error) {
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return nil, errors.New("报告查询时间范围无效")
	}
	location, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil {
		return nil, fmt.Errorf("报告查询时区无效: %w", err)
	}
	return location, nil
}

func newAPIFirstTokenMS(raw string) any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	values := make(map[string]any)
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&values); err != nil {
		return nil
	}
	value, ok := anyFloat(values["frt"])
	if !ok || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return value
}

func newQuotaDisplay(status newAPIStatus) (newAPIQuotaDisplay, error) {
	if status.QuotaPerUnit <= 0 || math.IsNaN(status.QuotaPerUnit) || math.IsInf(status.QuotaPerUnit, 0) {
		return newAPIQuotaDisplay{}, errors.New("New API quota_per_unit 无效")
	}
	display := newAPIQuotaDisplay{
		typeName:     strings.ToUpper(strings.TrimSpace(status.QuotaDisplayType)),
		quotaPerUnit: status.QuotaPerUnit, usdExchangeRate: status.USDExchangeRate,
		customExchangeRate: status.CustomCurrencyExchangeRate,
	}
	switch display.typeName {
	case "", "USD":
		display.typeName, display.unit = "USD", "USD"
	case "CNY":
		if display.usdExchangeRate <= 0 || math.IsNaN(display.usdExchangeRate) || math.IsInf(display.usdExchangeRate, 0) {
			return newAPIQuotaDisplay{}, errors.New("New API usd_exchange_rate 无效")
		}
		display.unit = "CNY"
	case "TOKENS":
		display.unit = "TOKENS"
	case "CUSTOM":
		if display.customExchangeRate <= 0 || math.IsNaN(display.customExchangeRate) || math.IsInf(display.customExchangeRate, 0) {
			return newAPIQuotaDisplay{}, errors.New("New API custom_currency_exchange_rate 无效")
		}
		display.unit = strings.TrimSpace(status.CustomCurrencySymbol)
		if display.unit == "" {
			display.unit = "CUSTOM"
		}
	default:
		return newAPIQuotaDisplay{}, fmt.Errorf("New API quota_display_type 不受支持: %s", display.typeName)
	}
	return display, nil
}

func (d newAPIQuotaDisplay) fromQuota(quota float64) float64 {
	if d.typeName == "TOKENS" {
		return quota
	}
	return d.fromUSD(quota / d.quotaPerUnit)
}

func (d newAPIQuotaDisplay) fromUSD(amount float64) float64 {
	switch d.typeName {
	case "CNY":
		return amount * d.usdExchangeRate
	case "TOKENS":
		return amount * d.quotaPerUnit
	case "CUSTOM":
		return amount * d.customExchangeRate
	default:
		return amount
	}
}
