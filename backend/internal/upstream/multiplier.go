package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const upstreamMultiplierBodyLimit = 2 << 20

// Sub2API limits one manual upstream billing probe batch to 20 accounts.
const upstreamMultiplierBatchSize = 20

// exportedAccount 是 Sub2API 管理员备份接口返回的单账号结构。
// Credentials 只在本次调用栈中使用，不持久化、不记录日志、不返回给前端。
type exportedAccount struct {
	Name        string         `json:"name"`
	Platform    string         `json:"platform"`
	Type        string         `json:"type"`
	Credentials map[string]any `json:"credentials"`
}

// AccountCredentials 是一次性读取的账号连接信息，仅供内部凭据匹配使用。
// 调用方不得将 APIKey 或 BaseURL 写入日志、缓存或 API 响应。
type AccountCredentials struct {
	BaseURL string `json:"-"`
	APIKey  string `json:"-"`
}

type accountExport struct {
	Accounts []exportedAccount `json:"accounts"`
}

type upstreamBillingProbeResult struct {
	AccountID int64                         `json:"account_id"`
	Snapshot  *upstreamBillingProbeSnapshot `json:"snapshot"`
	Error     string                        `json:"error"`
}

type upstreamBillingProbeSnapshot struct {
	Status    string         `json:"status"`
	Error     string         `json:"error"` // 兼容早期测试版字段。
	LastError string         `json:"last_error"`
	Data      map[string]any `json:"data"`
}

type upstreamBillingProbeBatchResponse struct {
	Results []upstreamBillingProbeResult `json:"results"`
}

// AccountUpstreamMultiplierResult 是批量原生探测中单个渠道的结果。
// Err 只在当前调用栈使用，不会序列化或持久化。
type AccountUpstreamMultiplierResult struct {
	AccountID  int64
	Multiplier float64
	Err        error
}

// FetchAccountUpstreamMultiplier 优先复用 Sub2API 自身的 API Key 上游计费探测。
// 新版 Sub2API 已支持所有 API Key 平台；旧版没有该接口时，才临时读取账号
// 连接信息并走兼容查询。
// 只有有限正数才会作为成功结果返回。
func (c *Client) FetchAccountUpstreamMultiplier(ctx context.Context, accountID int64, platform string) (float64, error) {
	if accountID <= 0 {
		return 0, errors.New("渠道 ID 无效")
	}
	// platform 参数保留给旧调用方；原生接口的平台判定由 Sub2API 自己完成，
	// Guardian 不再用本地白名单把新支持的平台误导向凭据导出路径。
	_ = platform
	value, available, err := c.fetchNativeUpstreamMultiplier(ctx, accountID)
	if available {
		return value, err
	}
	return c.fetchLegacyUpstreamMultiplier(ctx, accountID)
}

// ExportAccountCredentials 读取单个账号的原始连接信息。
// Sub2API 的普通账号接口会脱敏凭据，因此这里复用管理员导出接口；
// 返回值只在当前调用栈中存在。
func (c *Client) ExportAccountCredentials(ctx context.Context, accountID int64) (AccountCredentials, error) {
	account, err := c.exportedAccount(ctx, accountID)
	if err != nil {
		return AccountCredentials{}, sanitizeCredentialExportError(err)
	}
	if strings.TrimSpace(account.Type) != "" && !isAPIKeyAccountType(account.Type) {
		return AccountCredentials{}, errors.New("只有 API Key 类型渠道可以匹配 API Key")
	}
	credentials := account.Credentials
	apiKey := firstCredentialString(credentials, "api_key", "apiKey", "key", "token")
	baseURL := firstCredentialString(credentials, "base_url", "baseURL", "url")
	if apiKey == "" {
		return AccountCredentials{}, errors.New("渠道未配置 API Key，无法进行精确匹配")
	}
	if baseURL == "" {
		return AccountCredentials{}, errors.New("渠道未配置上游地址，无法进行精确匹配")
	}
	return AccountCredentials{BaseURL: baseURL, APIKey: apiKey}, nil
}

// sanitizeCredentialExportError 不把管理员导出接口的响应正文带出当前调用栈。
// 某些上游错误响应可能回显连接配置，联动日志只能保留状态或通用错误。
func sanitizeCredentialExportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return fmt.Errorf("读取渠道连接信息失败（HTTP %d）", apiErr.StatusCode)
	}
	return errors.New("读取渠道连接信息失败")
}

// fetchNativeUpstreamMultiplier 使用与 Sub2API 账户管理页面相同的探测接口。
// effective_rate_multiplier 是当前真正生效的倍率，已包含用户覆盖与峰值系数；
// 旧版响应没有该字段时兼容回退到 resolved_rate_multiplier。
func (c *Client) fetchNativeUpstreamMultiplier(ctx context.Context, accountID int64) (float64, bool, error) {
	var result upstreamBillingProbeResult
	path := fmt.Sprintf("/api/v1/admin/accounts/%d/upstream-billing-probe", accountID)
	if err := c.request(ctx, http.MethodPost, path, nil, &result); err != nil {
		switch StatusCodeOf(err) {
		case http.StatusNotFound, http.StatusMethodNotAllowed:
			return 0, false, nil
		default:
			return 0, true, fmt.Errorf("Sub2API 原生上游倍率探测失败: %w", err)
		}
	}

	value, err := multiplierFromProbeResult(result)
	return value, true, err
}

// FetchAccountUpstreamMultiplierBatch 使用 Sub2API 新版批量接口探测账号。
// available=false 表示管理端版本过旧，调用方应回退到单账号兼容路径。
// 单账号失败保存在对应结果的 Err 中，不影响同批其他账号。
func (c *Client) FetchAccountUpstreamMultiplierBatch(
	ctx context.Context,
	accountIDs []int64,
) (results map[int64]AccountUpstreamMultiplierResult, available bool, err error) {
	unique := make([]int64, 0, len(accountIDs))
	seen := make(map[int64]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if accountID <= 0 {
			return nil, true, errors.New("批量倍率探测包含无效渠道 ID")
		}
		if _, ok := seen[accountID]; ok {
			continue
		}
		seen[accountID] = struct{}{}
		unique = append(unique, accountID)
	}
	results = make(map[int64]AccountUpstreamMultiplierResult, len(unique))
	if len(unique) == 0 {
		return results, true, nil
	}

	for offset := 0; offset < len(unique); offset += upstreamMultiplierBatchSize {
		end := min(offset+upstreamMultiplierBatchSize, len(unique))
		chunk := unique[offset:end]
		var response upstreamBillingProbeBatchResponse
		requestBody := map[string]any{"account_ids": chunk}
		requestErr := c.request(ctx, http.MethodPost,
			"/api/v1/admin/accounts/upstream-billing-probe/batch", requestBody, &response)
		if requestErr != nil {
			switch StatusCodeOf(requestErr) {
			case http.StatusNotFound, http.StatusMethodNotAllowed:
				if offset == 0 {
					return nil, false, nil
				}
				return nil, true, errors.New("Sub2API 批量倍率接口在请求期间变为不可用")
			default:
				return nil, true, fmt.Errorf("Sub2API 原生批量上游倍率探测失败: %w", requestErr)
			}
		}

		requested := make(map[int64]struct{}, len(chunk))
		for _, accountID := range chunk {
			requested[accountID] = struct{}{}
		}
		for _, item := range response.Results {
			if _, ok := requested[item.AccountID]; !ok {
				return nil, true, fmt.Errorf("Sub2API 批量倍率接口返回了未请求的渠道 #%d", item.AccountID)
			}
			if _, duplicate := results[item.AccountID]; duplicate {
				return nil, true, fmt.Errorf("Sub2API 批量倍率接口重复返回渠道 #%d", item.AccountID)
			}
			value, itemErr := multiplierFromProbeResult(item)
			results[item.AccountID] = AccountUpstreamMultiplierResult{
				AccountID: item.AccountID, Multiplier: value, Err: itemErr,
			}
		}
		for _, accountID := range chunk {
			if _, ok := results[accountID]; !ok {
				results[accountID] = AccountUpstreamMultiplierResult{
					AccountID: accountID,
					Err:       errors.New("Sub2API 批量倍率接口未返回该渠道结果"),
				}
			}
		}
	}
	return results, true, nil
}

func multiplierFromProbeResult(result upstreamBillingProbeResult) (float64, error) {
	if detail := strings.TrimSpace(result.Error); detail != "" {
		return 0, fmt.Errorf("Sub2API 原生上游倍率探测失败: %s", detail)
	}
	if result.Snapshot == nil {
		return 0, errors.New("Sub2API 原生上游倍率探测失败: 未返回倍率快照")
	}
	if !strings.EqualFold(strings.TrimSpace(result.Snapshot.Status), "ok") {
		detail := strings.TrimSpace(result.Snapshot.LastError)
		if detail == "" {
			detail = strings.TrimSpace(result.Snapshot.Error)
		}
		if detail == "" {
			detail = "未返回成功快照"
		}
		return 0, fmt.Errorf("Sub2API 原生上游倍率探测失败: %s", detail)
	}
	if scope := strings.ToLower(credentialString(result.Snapshot.Data, "billing_scope")); scope != "" && scope != "token" {
		return 0, fmt.Errorf("Sub2API 返回不支持的计费范围 %q", scope)
	}
	for _, key := range []string{"effective_rate_multiplier", "resolved_rate_multiplier"} {
		value, ok := anyFloat(result.Snapshot.Data[key])
		if !ok {
			continue
		}
		if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, errors.New("Sub2API 原生探测返回非法倍率，继续使用原倍率")
		}
		return value, nil
	}
	return 0, errors.New("Sub2API 原生探测未返回有效倍率，继续使用原倍率")
}

func (c *Client) fetchLegacyUpstreamMultiplier(ctx context.Context, accountID int64) (float64, error) {
	account, err := c.exportedAccount(ctx, accountID)
	if err != nil {
		return 0, err
	}
	if !isAPIKeyAccountType(account.Type) {
		return 0, errors.New("只有 API Key 类型渠道可以同步上游倍率")
	}
	apiKey := credentialString(account.Credentials, "api_key")
	if apiKey == "" {
		return 0, errors.New("渠道未配置 API Key，无法读取上游倍率")
	}
	endpoint, err := multiplierEndpoint(account.Credentials)
	if err != nil {
		return 0, err
	}
	return requestMultiplier(ctx, endpoint, apiKey)
}

func (c *Client) exportedAccount(ctx context.Context, accountID int64) (exportedAccount, error) {
	if accountID <= 0 {
		return exportedAccount{}, errors.New("渠道 ID 无效")
	}
	var exported accountExport
	path := fmt.Sprintf("/api/v1/admin/accounts/data?ids=%d&include_proxies=false", accountID)
	if err := c.request(ctx, http.MethodGet, path, nil, &exported); err != nil {
		return exportedAccount{}, fmt.Errorf("读取渠道连接信息失败: %w", err)
	}
	if len(exported.Accounts) != 1 {
		return exportedAccount{}, fmt.Errorf("渠道 #%d 的连接信息不存在或不唯一", accountID)
	}
	return exported.Accounts[0], nil
}

func isAPIKeyAccountType(accountType string) bool {
	switch strings.ToLower(strings.TrimSpace(accountType)) {
	case "apikey", "api_key", "key":
		return true
	default:
		return false
	}
}

func credentialString(credentials map[string]any, key string) string {
	value, _ := credentials[key].(string)
	return strings.TrimSpace(value)
}

func firstCredentialString(credentials map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := credentialString(credentials, key); value != "" {
			return value
		}
	}
	return ""
}

// multiplierEndpoint 优先采用账号显式配置的倍率 URL；否则复用 API base_url
// 的部署前缀并访问 /v1/usage。后者是 Sub2API API Key 可鉴权的只读信息接口，
// 兼容返回 rate_multiplier / multiplier 等显式倍率字段的上游实现。
func multiplierEndpoint(credentials map[string]any) (*url.URL, error) {
	baseRaw := credentialString(credentials, "base_url")
	explicitRaw := credentialString(credentials, "rate_multiplier_url")
	if explicitRaw == "" {
		explicitRaw = credentialString(credentials, "multiplier_url")
	}

	if explicitRaw != "" {
		explicit, err := validateMultiplierURL(explicitRaw)
		if err != nil {
			return nil, fmt.Errorf("上游倍率地址无效: %w", err)
		}
		if baseRaw != "" {
			base, err := validateMultiplierURL(baseRaw)
			if err != nil {
				return nil, fmt.Errorf("上游地址无效: %w", err)
			}
			if !sameURLHost(base, explicit) {
				return nil, errors.New("倍率地址必须与渠道上游地址使用同一主机")
			}
		}
		return explicit, nil
	}

	if baseRaw == "" {
		return nil, errors.New("渠道未配置可查询倍率的上游地址")
	}
	base, err := validateMultiplierURL(baseRaw)
	if err != nil {
		return nil, fmt.Errorf("上游地址无效: %w", err)
	}
	path := strings.TrimRight(base.EscapedPath(), "/")
	if strings.HasSuffix(strings.ToLower(path), "/v1") {
		path += "/usage"
	} else {
		path += "/v1/usage"
	}
	base.Path, err = url.PathUnescape(path)
	if err != nil {
		return nil, errors.New("上游地址路径无效")
	}
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	return base, nil
}

func validateMultiplierURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New("无法解析 URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("只允许 http 或 https")
	}
	if u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("URL 不允许缺少主机、携带用户信息或片段")
	}
	hostname := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if hostname == "metadata.google.internal" {
		return nil, errors.New("不允许访问云主机元数据地址")
	}
	if ip := net.ParseIP(hostname); ip != nil {
		if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return nil, errors.New("不允许访问未指定、组播或链路本地地址")
		}
	}
	return u, nil
}

func sameURLHost(left, right *url.URL) bool {
	return strings.EqualFold(left.Hostname(), right.Hostname()) && effectivePort(left) == effectivePort(right)
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

func requestMultiplier(ctx context.Context, endpoint *url.URL, apiKey string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, errors.New("创建上游倍率请求失败")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-api-key", apiKey)

	transport := http.DefaultTransport.(*http.Transport).Clone()
	// 不交给环境代理解析目标主机，否则 DialContext 看到的只会是代理 IP，
	// 无法验证倍率 URL 最终解析到哪里。
	transport.Proxy = nil
	transport.DialContext = validatedDialContext(net.DefaultResolver)
	client := &http.Client{
		Timeout:   20 * time.Second,
		Transport: transport,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("上游倍率请求重定向次数过多")
			}
			if !sameURLHost(via[0].URL, next.URL) {
				return errors.New("上游倍率请求拒绝跨主机重定向")
			}
			if !strings.EqualFold(via[0].URL.Scheme, next.URL.Scheme) {
				return errors.New("上游倍率请求拒绝跨协议重定向")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("请求上游倍率失败: %w", sanitizeRequestError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return 0, fmt.Errorf("上游倍率接口返回 HTTP %d", resp.StatusCode)
	}

	decoder := json.NewDecoder(io.LimitReader(resp.Body, upstreamMultiplierBodyLimit))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return 0, errors.New("上游倍率接口未返回有效 JSON")
	}
	value, err := extractMultiplier(payload)
	if err != nil {
		return 0, err
	}
	return value, nil
}

type ipResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// validatedDialContext 在真正拨号前校验 DNS 结果，避免域名先通过文本校验、
// 随后解析到链路本地或云元数据地址。私网与回环地址有意保留，以支持本地代理。
func validatedDialContext(resolver ipResolver) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("上游倍率地址无效")
		}
		resolved, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("解析上游倍率主机失败: %w", err)
		}
		if len(resolved) == 0 {
			return nil, errors.New("上游倍率主机没有可用 IP")
		}
		for _, item := range resolved {
			if err := validateResolvedMultiplierIP(item.IP); err != nil {
				return nil, err
			}
		}

		var lastErr error
		for _, item := range resolved {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(item.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

func validateResolvedMultiplierIP(ip net.IP) error {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return errors.New("不允许访问未指定、组播或链路本地地址")
	}
	// 阿里云元数据服务使用 100.100.100.200，不属于链路本地网段。
	if ip.Equal(net.ParseIP("100.100.100.200")) {
		return errors.New("不允许访问云主机元数据地址")
	}
	return nil
}

// sanitizeRequestError 丢弃可能包含完整 URL 的 *url.Error，避免显式倍率 URL
// 将查询参数或其他敏感信息带进事件日志和前端错误提示。
func sanitizeRequestError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Err != nil {
			return urlErr.Err
		}
		return errors.New("网络请求失败")
	}
	return err
}

func extractMultiplier(payload any) (float64, error) {
	keys := map[string]bool{
		"rate_multiplier":    true,
		"multiplier":         true,
		"billing_multiplier": true,
		"group_multiplier":   true,
	}
	values := make([]float64, 0, 1)
	var visit func(any)
	visit = func(value any) {
		switch item := value.(type) {
		case map[string]any:
			for key, child := range item {
				if keys[strings.ToLower(strings.TrimSpace(key))] {
					if number, ok := anyFloat(child); ok && number > 0 && !math.IsNaN(number) && !math.IsInf(number, 0) {
						values = append(values, number)
					}
					continue
				}
				visit(child)
			}
		case []any:
			for _, child := range item {
				visit(child)
			}
		}
	}
	visit(payload)
	if len(values) == 0 {
		return 0, errors.New("上游未返回可识别的有效倍率，继续使用原倍率")
	}
	value := values[0]
	for _, candidate := range values[1:] {
		if math.Abs(candidate-value) > 1e-9 {
			return 0, errors.New("上游返回多个不一致倍率，继续使用原倍率")
		}
	}
	return value, nil
}
