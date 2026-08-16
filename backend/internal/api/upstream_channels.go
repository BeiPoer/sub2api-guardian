package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/channelmanager"
	"sub2api-guardian/backend/internal/store"
)

const upstreamSyncTimeout = 3 * time.Minute

type upstreamChannelPayload struct {
	Name              *string                    `json:"name"`
	Type              *store.UpstreamChannelType `json:"type"`
	BaseURL           *string                    `json:"base_url"`
	Username          *string                    `json:"username"`
	Password          *string                    `json:"password"`
	NewAPIAccessToken *string                    `json:"newapi_access_token"`
	NewAPIUserID      *string                    `json:"newapi_user_id"`
	Ignored           *bool                      `json:"ignored"`
	Sync              bool                       `json:"sync"`
}

type upstreamChannelListItem struct {
	store.UpstreamChannel
	LatestBalance *float64 `json:"latest_balance"`
	BalanceUnit   string   `json:"balance_unit"`
	TokenCount    int      `json:"token_count"`
}

func (s *Server) upstreamNoStore(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		next(w, r)
	}
}

func (s *Server) listUpstreamChannels(w http.ResponseWriter, _ *http.Request) {
	channels, err := s.store.UpstreamChannels()
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	items := make([]upstreamChannelListItem, 0, len(channels))
	active, ignored := 0, 0
	for _, channel := range channels {
		item := upstreamChannelListItem{UpstreamChannel: channel}
		if channel.Ignored {
			ignored++
		} else {
			active++
		}
		if snapshot, snapshotErr := s.store.LatestUpstreamBalanceSnapshot(channel.ID); snapshotErr == nil && snapshot != nil {
			balance := snapshot.Balance
			item.LatestBalance = &balance
			item.BalanceUnit = snapshot.Unit
		}
		if tokens, cacheErr := s.store.UpstreamCache(channel.ID, "tokens"); cacheErr == nil && tokens.Exists {
			if rows, ok := tokens.Value.([]any); ok {
				item.TokenCount = len(rows)
			}
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": len(items), "active": active, "ignored": ignored,
	})
}

func (s *Server) createUpstreamChannel(w http.ResponseWriter, r *http.Request) {
	var payload upstreamChannelPayload
	if err := decodeBody(r, &payload); err != nil {
		writeUpstreamError(w, err)
		return
	}
	input, err := normalizeUpstreamChannelPayload(payload, nil)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	channel, err := s.store.CreateUpstreamChannel(input)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if channel.Type != store.UpstreamChannelOther {
		ctx, cancel := context.WithTimeout(r.Context(), upstreamSyncTimeout)
		defer cancel()
		if err := s.upstreamChannels.Sync(ctx, channel.ID); err != nil {
			_ = s.store.DeleteUpstreamChannel(channel.ID)
			writeUpstreamError(w, err)
			return
		}
		channel, err = s.store.UpstreamChannel(channel.ID)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, channel)
}

func (s *Server) getUpstreamChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	channel, err := s.store.UpstreamChannel(id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, channel)
}

func (s *Server) updateUpstreamChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	existing, err := s.store.UpstreamChannel(id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	var payload upstreamChannelPayload
	if err := decodeBody(r, &payload); err != nil {
		writeUpstreamError(w, err)
		return
	}
	input, err := normalizeUpstreamChannelPayload(payload, &existing)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	channel, err := s.store.UpdateUpstreamChannel(id, input)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if payload.Sync && channel.Type != store.UpstreamChannelOther && !channel.Ignored {
		ctx, cancel := context.WithTimeout(r.Context(), upstreamSyncTimeout)
		defer cancel()
		if err := s.upstreamChannels.Sync(ctx, id); err != nil {
			writeUpstreamError(w, err)
			return
		}
		channel, _ = s.store.UpstreamChannel(id)
	}
	writeJSON(w, http.StatusOK, channel)
}

func normalizeUpstreamChannelPayload(payload upstreamChannelPayload, existing *store.UpstreamChannel) (store.UpstreamChannelInput, error) {
	input := store.UpstreamChannelInput{}
	if existing != nil {
		input = store.UpstreamChannelInput{
			Name: existing.Name, Type: existing.Type, BaseURL: existing.BaseURL,
			Username: existing.Username, Password: existing.Password,
			NewAPIAccessToken: existing.NewAPIAccessToken, NewAPIUserID: existing.NewAPIUserID,
			Ignored: existing.Ignored,
		}
	}
	if payload.Type != nil {
		if existing != nil && *payload.Type != existing.Type {
			return store.UpstreamChannelInput{}, &channelmanager.Error{Status: http.StatusBadRequest, Message: "渠道类型不可修改"}
		}
		input.Type = *payload.Type
	}
	if !input.Type.Valid() {
		return store.UpstreamChannelInput{}, &channelmanager.Error{Status: http.StatusBadRequest, Message: "渠道类型无效"}
	}
	if payload.BaseURL != nil {
		normalized, err := channelmanager.NormalizeBaseURL(*payload.BaseURL)
		if err != nil {
			return store.UpstreamChannelInput{}, err
		}
		input.BaseURL = normalized
	}
	if input.BaseURL == "" {
		return store.UpstreamChannelInput{}, &channelmanager.Error{Status: http.StatusBadRequest, Message: "站点链接不能为空"}
	}
	if payload.Name != nil {
		input.Name = strings.TrimSpace(*payload.Name)
	}
	if input.Name == "" {
		parsed, _ := url.Parse(input.BaseURL)
		input.Name = string(input.Type) + " " + parsed.Host
	}
	if payload.Username != nil {
		input.Username = strings.TrimSpace(*payload.Username)
	}
	if payload.Password != nil && *payload.Password != "" {
		input.Password = *payload.Password
	}
	if payload.NewAPIAccessToken != nil && *payload.NewAPIAccessToken != "" {
		input.NewAPIAccessToken = strings.TrimSpace(*payload.NewAPIAccessToken)
	}
	if payload.NewAPIUserID != nil {
		input.NewAPIUserID = strings.TrimSpace(*payload.NewAPIUserID)
	}
	if payload.Ignored != nil {
		input.Ignored = *payload.Ignored
	}
	if (input.Type == store.UpstreamChannelSub2API || input.Type == store.UpstreamChannelOther) && (input.Username == "" || input.Password == "") {
		label := "sub2api"
		if input.Type == store.UpstreamChannelOther {
			label = "其它"
		}
		return store.UpstreamChannelInput{}, &channelmanager.Error{Status: http.StatusBadRequest, Message: label + "渠道需要账号和密码"}
	}
	if input.Type == store.UpstreamChannelNewAPI && (input.NewAPIAccessToken == "" || input.NewAPIUserID == "") {
		return store.UpstreamChannelInput{}, &channelmanager.Error{Status: http.StatusBadRequest, Message: "new-api 渠道需要系统访问令牌和 userId"}
	}
	return input, nil
}

func (s *Server) deleteUpstreamChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		err = s.store.DeleteUpstreamChannel(id)
	}
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) syncUpstreamChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamSyncTimeout)
	defer cancel()
	if err := s.upstreamChannels.Sync(ctx, id); err != nil {
		writeUpstreamError(w, err)
		return
	}
	channel, err := s.store.UpstreamChannel(id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": channel})
}

func (s *Server) syncAllUpstreamChannels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	ok, failed, err := s.upstreamChannels.SyncAll(ctx)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": failed == 0, "synced": ok, "failed": failed})
}

func (s *Server) loginUpstreamChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamSyncTimeout)
	defer cancel()
	target, err := s.upstreamChannels.LoginURL(ctx, id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) upstreamChannelOverview(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	overview, err := s.upstreamChannels.Overview(id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (s *Server) upstreamChannelGroups(w http.ResponseWriter, r *http.Request) {
	s.writeUpstreamCache(w, r, "groups", []any{})
}

func (s *Server) upstreamChannelTokens(w http.ResponseWriter, r *http.Request) {
	s.writeUpstreamCache(w, r, "tokens", []any{})
}

func (s *Server) upstreamChannelSubscriptions(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	channel, err := s.store.UpstreamChannel(id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if channel.Type != store.UpstreamChannelSub2API {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	s.writeUpstreamCache(w, r, "subscriptions", nil)
}

func (s *Server) writeUpstreamCache(w http.ResponseWriter, r *http.Request, key string, fallback any) {
	id, err := pathID(r)
	if err == nil {
		_, err = s.store.UpstreamChannel(id)
	}
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	entry, err := s.store.UpstreamCache(id, key)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if !entry.Exists {
		writeJSON(w, http.StatusOK, fallback)
		return
	}
	writeJSON(w, http.StatusOK, entry.Value)
}

func (s *Server) upstreamBalanceHistory(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		_, err = s.store.UpstreamChannel(id)
	}
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	items, err := s.store.UpstreamBalanceHistory(id, queryInt(r, "limit", 200))
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) upstreamTokenModels(w http.ResponseWriter, r *http.Request) {
	channelID, err := pathID(r)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	tokenID, err := upstreamPathID(r, "tokenId")
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamSyncTimeout)
	defer cancel()
	result, err := s.upstreamChannels.TokenModels(ctx, channelID, tokenID)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) updateUpstreamTokenGroup(w http.ResponseWriter, r *http.Request) {
	channelID, err := pathID(r)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	tokenID, err := upstreamPathID(r, "tokenId")
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	var payload map[string]any
	if err := decodeBody(r, &payload); err != nil {
		writeUpstreamError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamSyncTimeout)
	defer cancel()
	result, err := s.upstreamChannels.UpdateTokenGroup(ctx, channelID, tokenID, payload)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type upstreamTaskPayload struct {
	Type            *store.UpstreamTaskType `json:"type"`
	Enabled         *bool                   `json:"enabled"`
	IntervalMinutes *int                    `json:"interval_minutes"`
	Threshold       *float64                `json:"threshold"`
	LookbackMinutes *int                    `json:"lookback_minutes"`
	CooldownMinutes *int                    `json:"cooldown_minutes"`
	Recipients      *[]string               `json:"recipients"`
}

func (s *Server) listUpstreamTasks(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		_, err = s.store.UpstreamChannel(id)
	}
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	items, err := s.store.UpstreamAutomationTasks(id)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createUpstreamTask(w http.ResponseWriter, r *http.Request) {
	channelID, err := pathID(r)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	channel, err := s.store.UpstreamChannel(channelID)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if channel.Type == store.UpstreamChannelOther {
		writeUpstreamError(w, &channelmanager.Error{Status: http.StatusBadRequest, Message: "其它渠道仅用于记录，不支持配置自动化告警"})
		return
	}
	var payload upstreamTaskPayload
	if err := decodeBody(r, &payload); err != nil {
		writeUpstreamError(w, err)
		return
	}
	if payload.Type == nil || !payload.Type.Valid() {
		writeUpstreamError(w, &channelmanager.Error{Status: http.StatusBadRequest, Message: "任务类型无效"})
		return
	}
	settings, _ := s.store.UpstreamEmailSettings()
	task := store.UpstreamAutomationTask{
		ChannelID: channelID, Type: *payload.Type, Enabled: true, LookbackMinutes: 60, Recipients: []string{},
		IntervalMinutes: settings.DefaultIntervalMinutes, CooldownMinutes: 60,
	}
	if task.Type == store.UpstreamTaskLowBalance {
		task.IntervalMinutes = 5
		task.CooldownMinutes = 30
	}
	if err := applyUpstreamTaskPayload(&task, payload, true); err != nil {
		writeUpstreamError(w, err)
		return
	}
	created, err := s.store.CreateUpstreamAutomationTask(task)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if err := s.upstreamChannels.SeedTaskState(created); err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateUpstreamTask(w http.ResponseWriter, r *http.Request) {
	channelID, err := pathID(r)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	taskID, err := upstreamPathID(r, "taskId")
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	existing, err := s.store.UpstreamAutomationTask(channelID, taskID)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	var payload upstreamTaskPayload
	if err := decodeBody(r, &payload); err != nil {
		writeUpstreamError(w, err)
		return
	}
	task := existing
	if err := applyUpstreamTaskPayload(&task, payload, false); err != nil {
		writeUpstreamError(w, err)
		return
	}
	updated, err := s.store.UpdateUpstreamAutomationTask(task)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	changedToGroup := !existing.Type.IsGroupTask() && updated.Type.IsGroupTask()
	reenabledGroup := updated.Type.IsGroupTask() && !existing.Enabled && updated.Enabled
	if changedToGroup || reenabledGroup {
		if err := s.upstreamChannels.SeedTaskState(updated); err != nil {
			writeUpstreamError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, updated)
}

func applyUpstreamTaskPayload(task *store.UpstreamAutomationTask, payload upstreamTaskPayload, creating bool) error {
	if payload.Type != nil {
		if !payload.Type.Valid() {
			return &channelmanager.Error{Status: http.StatusBadRequest, Message: "任务类型无效"}
		}
		task.Type = *payload.Type
	}
	if payload.Enabled != nil {
		task.Enabled = *payload.Enabled
	}
	if payload.IntervalMinutes != nil {
		task.IntervalMinutes = *payload.IntervalMinutes
	}
	if payload.Threshold != nil {
		task.Threshold = *payload.Threshold
	} else if creating && !task.Type.IsGroupTask() {
		return &channelmanager.Error{Status: http.StatusBadRequest, Message: "预警阈值无效"}
	}
	if payload.LookbackMinutes != nil {
		task.LookbackMinutes = *payload.LookbackMinutes
	}
	if payload.CooldownMinutes != nil {
		task.CooldownMinutes = *payload.CooldownMinutes
	}
	if task.IntervalMinutes <= 0 || task.LookbackMinutes <= 0 || task.CooldownMinutes < 0 {
		return &channelmanager.Error{Status: http.StatusBadRequest, Message: "任务时间参数无效"}
	}
	if payload.Recipients != nil {
		recipients, err := normalizeRecipientList(*payload.Recipients)
		if err != nil {
			return err
		}
		task.Recipients = recipients
	}
	return nil
}

func (s *Server) deleteUpstreamTask(w http.ResponseWriter, r *http.Request) {
	channelID, err := pathID(r)
	if err == nil {
		var taskID int64
		taskID, err = upstreamPathID(r, "taskId")
		if err == nil {
			err = s.store.DeleteUpstreamAutomationTask(channelID, taskID)
		}
	}
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) upstreamBalanceLogs(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		_, err = s.store.UpstreamChannel(id)
	}
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	items, total, page, pageSize, err := s.store.UpstreamBalanceQueryLogs(id, queryInt(r, "page", 1), queryInt(r, "page_size", 20))
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	pages := max(1, int((total+int64(pageSize)-1)/int64(pageSize)))
	writeJSON(w, http.StatusOK, channelmanager.Page[store.UpstreamBalanceQueryLog]{Items: items, Total: total, Page: page, PageSize: pageSize, Pages: pages})
}

func (s *Server) upstreamAlerts(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		_, err = s.store.UpstreamChannel(id)
	}
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	items, err := s.store.UpstreamAlertEvents(id, 200)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type upstreamEmailPayload struct {
	SMTPHost               *string   `json:"smtp_host"`
	SMTPPort               *int      `json:"smtp_port"`
	SMTPSecure             *bool     `json:"smtp_secure"`
	SMTPUser               *string   `json:"smtp_user"`
	SMTPPassword           *string   `json:"smtp_password"`
	SMTPFrom               *string   `json:"smtp_from"`
	SubjectPrefix          *string   `json:"subject_prefix"`
	DefaultRecipients      *[]string `json:"default_recipients"`
	DefaultIntervalMinutes *int      `json:"default_interval_minutes"`
}

func (s *Server) getUpstreamEmailSettings(w http.ResponseWriter, _ *http.Request) {
	settings, err := s.upstreamChannels.EmailSettings()
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) saveUpstreamEmailSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.upstreamChannels.EmailSettings()
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	var payload upstreamEmailPayload
	if err := decodeBody(r, &payload); err != nil {
		writeUpstreamError(w, err)
		return
	}
	if payload.SMTPHost != nil {
		settings.SMTPHost = strings.TrimSpace(*payload.SMTPHost)
	}
	if payload.SMTPPort != nil {
		settings.SMTPPort = *payload.SMTPPort
	}
	if payload.SMTPSecure != nil {
		settings.SMTPSecure = *payload.SMTPSecure
	}
	if payload.SMTPUser != nil {
		settings.SMTPUser = strings.TrimSpace(*payload.SMTPUser)
	}
	if payload.SMTPPassword != nil && *payload.SMTPPassword != "" {
		settings.SMTPPassword = *payload.SMTPPassword
	}
	if payload.SMTPFrom != nil {
		settings.SMTPFrom = strings.TrimSpace(*payload.SMTPFrom)
	}
	if payload.SubjectPrefix != nil {
		settings.SubjectPrefix = strings.TrimSpace(*payload.SubjectPrefix)
	}
	if payload.DefaultRecipients != nil {
		settings.DefaultRecipients, err = normalizeRecipientList(*payload.DefaultRecipients)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
	}
	if payload.DefaultIntervalMinutes != nil {
		settings.DefaultIntervalMinutes = *payload.DefaultIntervalMinutes
	}
	saved, err := s.upstreamChannels.SaveEmailSettings(settings)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) testUpstreamEmailSettings(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Recipients []string `json:"recipients"`
	}
	if err := decodeBody(r, &payload); err != nil {
		writeUpstreamError(w, err)
		return
	}
	recipients, err := normalizeRecipientList(payload.Recipients)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	messageID, err := s.upstreamChannels.TestEmail(ctx, recipients)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message_id": messageID})
}

func normalizeRecipientList(values []string) ([]string, error) {
	seen := make(map[string]bool)
	result := make([]string, 0)
	for _, value := range values {
		for _, candidate := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' }) {
			if strings.ContainsAny(candidate, "\r\n") {
				return nil, &channelmanager.Error{Status: http.StatusBadRequest, Message: "收件人不能包含换行符"}
			}
			address, err := mail.ParseAddress(strings.TrimSpace(candidate))
			if err != nil || address.Address == "" {
				return nil, &channelmanager.Error{Status: http.StatusBadRequest, Message: "收件人邮箱格式无效：" + candidate}
			}
			if !seen[address.Address] {
				seen[address.Address] = true
				result = append(result, address.Address)
			}
		}
	}
	return result, nil
}

func upstreamPathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue(name)), 10, 64)
	if err != nil || id <= 0 {
		return 0, &channelmanager.Error{Status: http.StatusBadRequest, Message: "无效的 ID"}
	}
	return id, nil
}

func writeUpstreamError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "上游渠道操作失败"
	var managed *channelmanager.Error
	switch {
	case errors.As(err, &managed):
		status = managed.Status
		message = managed.Message
	case errors.Is(err, store.ErrUpstreamChannelNotFound), errors.Is(err, store.ErrUpstreamTaskNotFound):
		status = http.StatusNotFound
		message = err.Error()
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		status = http.StatusGatewayTimeout
		message = "上游操作超时"
	case err != nil:
		log.Printf("上游渠道内部错误: %v", err)
	}
	writeJSON(w, status, map[string]any{"error": message})
}
