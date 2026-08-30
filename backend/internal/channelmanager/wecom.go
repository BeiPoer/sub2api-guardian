package channelmanager

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/wecom"
)

const wecomAPIBaseURL = wecom.DefaultAPIBaseURL

func (m *Manager) WeComSettings() (store.UpstreamWeComSettings, error) {
	return m.store.UpstreamWeComSettings()
}

func (m *Manager) SaveWeComSettings(settings store.UpstreamWeComSettings) (store.UpstreamWeComSettings, error) {
	current, err := m.store.UpstreamWeComSettings()
	if err != nil {
		return store.UpstreamWeComSettings{}, err
	}
	if settings.Secret == "" {
		settings.Secret = current.Secret
	}
	if err := validateWeComSettings(settings, false); err != nil {
		return store.UpstreamWeComSettings{}, err
	}
	return m.store.SaveUpstreamWeComSettings(settings)
}

func (m *Manager) TestWeCom(ctx context.Context, target string) (string, error) {
	return m.sendWeCom(ctx, target, "Sub2API Guardian 企微通知测试")
}

func (m *Manager) sendWeCom(ctx context.Context, target, content string) (string, error) {
	settings, err := m.store.UpstreamWeComSettings()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(target) != "" {
		settings.Target = strings.TrimSpace(target)
	}
	if err := validateWeComSettings(settings, true); err != nil {
		return "", err
	}
	if m.wecomClient == nil {
		m.wecomClient = wecom.New(m.client)
	}
	m.wecomClient.SetBaseURL(m.wecomBaseURL)
	id, err := m.wecomClient.Send(ctx, wecom.Settings{
		CorpID: settings.CorpID, AgentID: settings.AgentID,
		Secret: settings.Secret, Target: settings.Target,
	}, wecom.Text, content)
	if err != nil {
		return "", exposeWeComError(err)
	}
	return id, nil
}

func exposeWeComError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *wecom.APIError
	if errors.As(err, &apiErr) {
		return &Error{Status: http.StatusBadGateway, UpstreamCode: apiErr.Code, Message: apiErr.Error()}
	}
	return err
}

func validateWeComSettings(settings store.UpstreamWeComSettings, requireComplete bool) error {
	if err := wecom.Validate(wecom.Settings{
		CorpID: settings.CorpID, AgentID: settings.AgentID,
		Secret: settings.Secret, Target: settings.Target,
	}, requireComplete); err != nil {
		return invalid(err.Error())
	}
	return nil
}
