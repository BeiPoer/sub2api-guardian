// Package channelmanager 管理独立的上游渠道目录；目录数据隔离，倍率可通过回调联动 Guardian 调度。
package channelmanager

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"sub2api-guardian/backend/internal/store"
)

// Error 是可安全返回给面板的业务/上游错误。
// 上游 HTTP 401 统一映射成 502，避免前端误清除 Guardian 会话。
type Error struct {
	Status       int
	UpstreamCode int
	Message      string
	Details      any
}

func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return nil }

func appError(status int, message string) error {
	return &Error{Status: status, Message: message}
}

func upstreamError(status int, message string, details any) error {
	if status == http.StatusUnauthorized {
		return &Error{Status: http.StatusBadGateway, UpstreamCode: status, Message: message, Details: details}
	}
	if status < 400 {
		status = http.StatusBadGateway
	}
	return &Error{Status: status, UpstreamCode: status, Message: message, Details: details}
}

// isManualTokenAuthError 只识别手动凭据渠道的鉴权错误，兼容 HTTP 200 的业务错误。
func isManualTokenAuthError(channel store.UpstreamChannel, err error) bool {
	if !((channel.Type == store.UpstreamChannelSub2API && channel.Sub2APIManualAccessToken != "") ||
		(channel.Type == store.UpstreamChannelNewAPI && channel.NewAPIAccessToken != "")) {
		return false
	}
	if isUpstreamStatus(err, http.StatusUnauthorized, http.StatusForbidden) {
		return true
	}
	var target *Error
	if !errors.As(err, &target) || target.UpstreamCode != 0 {
		return false
	}
	details, ok := asObject(target.Details)
	if !ok {
		return false
	}
	switch strings.ToUpper(stringValue(details["code"])) {
	case "401", "403", "TOKEN_EXPIRED", "INVALID_TOKEN", "TOKEN_REVOKED",
		"AUTH_TOKEN_EXPIRED", "AUTH_UNAUTHORIZED", "AUTH_SESSION_REVOKED":
		return true
	}
	message := strings.ToLower(target.Message)
	for _, text := range []string{"invalid access token", "access token 无效", "access token 無效",
		"invalid token", "token expired", "token has expired", "token has been revoked",
		"token已失效", "token 已失效", "token已过期", "token 已过期", "令牌已过期", "令牌无效"} {
		if strings.Contains(message, text) {
			return true
		}
	}
	return false
}

func channelError(err error) error {
	if errors.Is(err, store.ErrUpstreamChannelNotFound) {
		return &Error{Status: http.StatusNotFound, Message: err.Error()}
	}
	if errors.Is(err, store.ErrUpstreamTaskNotFound) {
		return &Error{Status: http.StatusNotFound, Message: err.Error()}
	}
	return err
}

func invalid(message string) error { return appError(http.StatusBadRequest, message) }

func formatUpstreamStatus(status int, message string) string {
	if message == "" {
		message = "上游请求失败"
	}
	return fmt.Sprintf("%s（HTTP %d）", message, status)
}

type TokenModelsResult struct {
	TokenID   int64    `json:"token_id"`
	TokenName string   `json:"token_name"`
	Source    string   `json:"source"`
	Models    []string `json:"models"`
}

type UpstreamGroupRatioChange struct {
	Key       string  `json:"key"`
	Label     string  `json:"label"`
	Before    float64 `json:"before"`
	After     float64 `json:"after"`
	ChangedAt string  `json:"changed_at"`
}

type Overview struct {
	Channel                 store.UpstreamChannel           `json:"channel"`
	Profile                 any                             `json:"profile"`
	Groups                  any                             `json:"groups"`
	Tokens                  any                             `json:"tokens"`
	Subscriptions           any                             `json:"subscriptions"`
	LatestSnapshot          *store.UpstreamBalanceSnapshot  `json:"latest_snapshot"`
	History                 []store.UpstreamBalanceSnapshot `json:"history"`
	RecentGroupRatioChanges []UpstreamGroupRatioChange      `json:"recent_group_ratio_changes"`
}

type Page[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Pages    int   `json:"pages"`
}
