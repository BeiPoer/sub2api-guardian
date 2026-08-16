// Package channelmanager 管理独立的上游渠道目录，不参与 Guardian 自身渠道调度。
package channelmanager

import (
	"errors"
	"fmt"
	"net/http"

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

type Overview struct {
	Channel        store.UpstreamChannel           `json:"channel"`
	Profile        any                             `json:"profile"`
	Groups         any                             `json:"groups"`
	Tokens         any                             `json:"tokens"`
	Subscriptions  any                             `json:"subscriptions"`
	LatestSnapshot *store.UpstreamBalanceSnapshot  `json:"latest_snapshot"`
	History        []store.UpstreamBalanceSnapshot `json:"history"`
}

type Page[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Pages    int   `json:"pages"`
}
