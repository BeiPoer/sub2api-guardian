package reports

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
	"sub2api-guardian/backend/internal/wecom"
)

type Manager struct {
	store  *store.Store
	client *upstream.Client
	wecom  *wecom.Client

	runMu sync.Mutex

	stop      chan struct{}
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
	started   bool
	stateMu   sync.Mutex
}

func New(st *store.Store, client *upstream.Client) *Manager {
	return &Manager{
		store: st, client: client,
		wecom: wecom.New(&http.Client{Timeout: 45 * time.Second}),
		stop:  make(chan struct{}), done: make(chan struct{}),
	}
}

func (m *Manager) Start() {
	m.startOnce.Do(func() {
		m.stateMu.Lock()
		m.started = true
		m.stateMu.Unlock()
		go m.loop()
	})
}

func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		m.stateMu.Lock()
		started := m.started
		m.stateMu.Unlock()
		if !started {
			return
		}
		close(m.stop)
		<-m.done
	})
}

func (m *Manager) loop() {
	defer close(m.done)
	_ = m.store.CleanupScheduledReportRuns(time.Now().Add(-runHistoryRetentionHours * time.Hour))
	m.runDue(context.Background())
	ticker := time.NewTicker(time.Minute)
	cleanup := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	defer cleanup.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			m.runDue(context.Background())
		case <-cleanup.C:
			_ = m.store.CleanupScheduledReportRuns(time.Now().Add(-runHistoryRetentionHours * time.Hour))
		}
	}
}

func (m *Manager) View() (View, error) {
	report, exists, err := m.store.ScheduledReport(store.ScheduledReportChannelUsage)
	if err != nil {
		return View{}, err
	}
	if !exists {
		report = defaultScheduledReport()
	}
	config, err := decodeStoredConfig(report.ConfigJSON)
	if err != nil {
		return View{}, err
	}
	return m.viewFor(report, config)
}

func (m *Manager) DailyView() (DailyView, error) {
	report, exists, err := m.store.ScheduledReport(store.ScheduledReportDaily)
	if err != nil {
		return DailyView{}, err
	}
	if !exists {
		report = defaultDailyReport()
	}
	return m.dailyViewFor(report)
}

func (m *Manager) Save(input SaveInput) (View, error) {
	if err := validateSaveInput(input); err != nil {
		return View{}, err
	}
	if _, err := m.notificationSettings(); err != nil {
		return View{}, err
	}
	report, exists, err := m.store.ScheduledReport(store.ScheduledReportChannelUsage)
	if err != nil {
		return View{}, err
	}
	config := storedConfig{
		LookbackHours:         input.LookbackHours,
		FirstTokenThresholdMS: input.FirstTokenThresholdMS,
		TriggerCount:          input.TriggerCount,
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return View{}, err
	}
	if !exists {
		report = defaultScheduledReport()
	}
	report.Type = store.ScheduledReportChannelUsage
	report.Enabled = input.Enabled
	report.IntervalMinutes = input.IntervalMinutes
	report.StartHour = input.StartHour
	report.EndHour = input.EndHour
	report.Timezone = strings.TrimSpace(input.Timezone)
	report.ConfigJSON = string(raw)
	saved, err := m.store.SaveScheduledReportConfig(report)
	if err != nil {
		return View{}, err
	}
	return m.viewFor(saved, config)
}

func (m *Manager) SaveDaily(input DailySaveInput) (DailyView, error) {
	if err := validateDailySaveInput(input); err != nil {
		return DailyView{}, err
	}
	if _, err := m.notificationSettings(); err != nil {
		return DailyView{}, err
	}
	report, exists, err := m.store.ScheduledReport(store.ScheduledReportDaily)
	if err != nil {
		return DailyView{}, err
	}
	if !exists {
		report = defaultDailyReport()
	}
	report.Type = store.ScheduledReportDaily
	report.Enabled = input.Enabled
	report.IntervalMinutes = 24 * 60
	report.StartHour = input.RunHour
	report.EndHour = input.RunHour
	report.Timezone = strings.TrimSpace(input.Timezone)
	report.ConfigJSON = `{}`
	saved, err := m.store.SaveScheduledReportConfig(report)
	if err != nil {
		return DailyView{}, err
	}
	return m.dailyViewFor(saved)
}

func (m *Manager) NotificationSettings() (NotificationConfig, error) {
	settings, err := m.notificationSettings()
	if err != nil {
		return NotificationConfig{}, err
	}
	return notificationConfig(settings), nil
}

func (m *Manager) SaveNotificationSettings(input NotificationSaveInput) (NotificationConfig, error) {
	current, err := m.notificationSettings()
	if err != nil {
		return NotificationConfig{}, err
	}
	secret := strings.TrimSpace(input.WeCom.Secret)
	if secret == "" {
		secret = current.WeCom.Secret
	}
	settings := store.ScheduledReportNotificationSettings{
		WeCom: store.ScheduledReportWeComSettings{
			Enabled: input.WeCom.Enabled,
			CorpID:  strings.TrimSpace(input.WeCom.CorpID),
			AgentID: input.WeCom.AgentID,
			Secret:  secret,
			Target:  strings.TrimSpace(input.WeCom.Target),
		},
	}
	if err := validateNotificationSettings(settings, settings.WeCom.Enabled); err != nil {
		return NotificationConfig{}, err
	}
	saved, err := m.store.SaveScheduledReportNotificationSettings(settings)
	if err != nil {
		return NotificationConfig{}, err
	}
	return notificationConfig(saved), nil
}

func (m *Manager) Runs(page, pageSize int) ([]store.ScheduledReportRun, int64, int, int, int, error) {
	return m.runsFor(store.ScheduledReportChannelUsage, page, pageSize)
}

func (m *Manager) DailyRuns(page, pageSize int) ([]store.ScheduledReportRun, int64, int, int, int, error) {
	return m.runsFor(store.ScheduledReportDaily, page, pageSize)
}

func (m *Manager) runsFor(reportType store.ScheduledReportType, page, pageSize int) ([]store.ScheduledReportRun, int64, int, int, int, error) {
	report, exists, err := m.store.ScheduledReport(reportType)
	if err != nil {
		return nil, 0, 0, 0, 0, err
	}
	if !exists {
		if page < 1 {
			page = 1
		}
		if pageSize < 1 || pageSize > 100 {
			pageSize = 20
		}
		return []store.ScheduledReportRun{}, 0, page, pageSize, 0, nil
	}
	items, total, page, pageSize, err := m.store.ScheduledReportRuns(report.ID, page, pageSize)
	if err != nil {
		return nil, 0, 0, 0, 0, err
	}
	pages := int((total + int64(pageSize) - 1) / int64(pageSize))
	return items, total, page, pageSize, pages, nil
}

func (m *Manager) RunNow(ctx context.Context) (store.ScheduledReportRun, error) {
	if !m.runMu.TryLock() {
		return store.ScheduledReportRun{}, ErrAlreadyRunning
	}
	defer m.runMu.Unlock()

	report, exists, err := m.store.ScheduledReport(store.ScheduledReportChannelUsage)
	if err != nil {
		return store.ScheduledReportRun{}, err
	}
	if !exists {
		report = defaultScheduledReport()
		saved, saveErr := m.store.SaveScheduledReportConfig(report)
		if saveErr != nil {
			return store.ScheduledReportRun{}, saveErr
		}
		report = saved
	}
	config, err := decodeStoredConfig(report.ConfigJSON)
	if err != nil {
		return store.ScheduledReportRun{}, err
	}
	return m.execute(ctx, report, config)
}

func (m *Manager) RunDailyNow(ctx context.Context) (store.ScheduledReportRun, error) {
	if !m.runMu.TryLock() {
		return store.ScheduledReportRun{}, ErrAlreadyRunning
	}
	defer m.runMu.Unlock()

	report, exists, err := m.store.ScheduledReport(store.ScheduledReportDaily)
	if err != nil {
		return store.ScheduledReportRun{}, err
	}
	if !exists {
		report = defaultDailyReport()
		saved, saveErr := m.store.SaveScheduledReportConfig(report)
		if saveErr != nil {
			return store.ScheduledReportRun{}, saveErr
		}
		report = saved
	}
	return m.executeDaily(ctx, report)
}

func (m *Manager) TestNotification(ctx context.Context) (string, error) {
	settings, err := m.notificationSettings()
	if err != nil {
		return "", err
	}
	if err := validateNotificationSettings(settings, true); err != nil {
		return "", err
	}
	wecomSettings := toWeComSettings(settings.WeCom)
	messageID, err := m.wecom.Send(ctx, wecomSettings, wecom.Text, buildTestText(time.Now(), time.Local))
	if err != nil {
		return "", err
	}
	return messageID, nil
}

func (m *Manager) runDue(ctx context.Context) {
	m.runChannelUsageDue(ctx)
	m.runDailyDue(ctx)
}

func (m *Manager) runChannelUsageDue(ctx context.Context) {
	report, exists, err := m.store.ScheduledReport(store.ScheduledReportChannelUsage)
	if err != nil || !exists || !report.Enabled {
		return
	}
	config, err := decodeStoredConfig(report.ConfigJSON)
	if err != nil {
		return
	}
	location, err := time.LoadLocation(report.Timezone)
	if err != nil {
		return
	}
	now := time.Now()
	localNow := now.In(location)
	if !withinWindow(localNow, report.StartHour, report.EndHour) {
		return
	}
	if report.LastRunAt != "" {
		last, parseErr := time.Parse(time.RFC3339Nano, report.LastRunAt)
		if parseErr == nil && now.Sub(last) < time.Duration(report.IntervalMinutes)*time.Minute {
			return
		}
	}
	if !m.runMu.TryLock() {
		return
	}
	defer m.runMu.Unlock()
	_, _ = m.execute(ctx, report, config)
}

func (m *Manager) runDailyDue(ctx context.Context) {
	report, exists, err := m.store.ScheduledReport(store.ScheduledReportDaily)
	if err != nil || !exists || !report.Enabled {
		return
	}
	location, err := time.LoadLocation(report.Timezone)
	if err != nil {
		return
	}
	now := time.Now()
	localNow := now.In(location)
	if !withinWindow(localNow, report.StartHour, report.EndHour) {
		return
	}
	if report.LastRunAt != "" {
		last, parseErr := time.Parse(time.RFC3339Nano, report.LastRunAt)
		if parseErr == nil && now.Sub(last) < 24*time.Hour {
			return
		}
	}
	if !m.runMu.TryLock() {
		return
	}
	defer m.runMu.Unlock()
	_, _ = m.executeDaily(ctx, report)
}

func (m *Manager) execute(ctx context.Context, report store.ScheduledReport, config storedConfig) (store.ScheduledReportRun, error) {
	startedAt := time.Now().UTC()
	location, err := time.LoadLocation(report.Timezone)
	if err != nil {
		return store.ScheduledReportRun{}, err
	}
	windowEnd := startedAt
	windowStart := windowEnd.Add(-time.Duration(config.LookbackHours) * time.Hour)
	run := store.ScheduledReportRun{
		ReportID:           report.ID,
		StartedAt:          formatUTC(startedAt),
		WindowStart:        formatUTC(windowStart),
		WindowEnd:          formatUTC(windowEnd),
		NotificationStatus: "not_needed",
	}
	notificationSettings, notificationErr := m.notificationSettings()

	queryCtx, cancel := context.WithTimeout(ctx, usageRequestTimeout*time.Second)
	records, queryErr := m.client.ListUsage(queryCtx, windowStart, windowEnd, report.Timezone)
	cancel()
	if queryErr != nil {
		run.Status = "error"
		run.Error = safeError(queryErr)
		run.Message = "查询 usage 失败"
		m.sendFailure("渠道使用报告", notificationSettings, notificationErr, location, startedAt, queryErr, &run)
		return m.finish(report, run, startedAt)
	}

	evaluation := Evaluate(records, windowStart, windowEnd, location, float64(config.FirstTokenThresholdMS), config.TriggerCount)
	run.TotalRecords = evaluation.TotalRecords
	run.HighLatencyCount = evaluation.HighLatencyCount
	run.Summary = evaluation.Rows
	if evaluation.Alert {
		run.Status = "alert"
		run.Message = "高延迟数量超过触发条数"
		if notificationErr != nil {
			run.NotificationStatus = "failed"
			run.NotificationError = safeError(notificationErr)
			run.Message = "告警生成成功，但通知配置读取失败"
		} else if settings, ok := completeWeComSettings(notificationSettings); ok {
			_, sendErr := m.wecom.Send(ctx, settings, wecom.Text, buildAlertText(evaluation, startedAt, windowStart, windowEnd, location, config.FirstTokenThresholdMS, config.TriggerCount))
			if sendErr != nil {
				run.NotificationStatus = "failed"
				run.NotificationError = safeError(sendErr)
				run.Message = "告警生成成功，但企微投递失败"
			} else {
				run.NotificationStatus = "sent"
			}
		} else {
			run.NotificationStatus = "disabled"
		}
	} else {
		run.Status = "ok"
		run.Message = "无告警"
	}
	return m.finish(report, run, startedAt)
}

func (m *Manager) executeDaily(ctx context.Context, report store.ScheduledReport) (store.ScheduledReportRun, error) {
	startedAt := time.Now().UTC()
	location, err := time.LoadLocation(report.Timezone)
	if err != nil {
		return store.ScheduledReportRun{}, err
	}
	localNow := startedAt.In(location)
	windowStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, location)
	run := store.ScheduledReportRun{
		ReportID:           report.ID,
		StartedAt:          formatUTC(startedAt),
		WindowStart:        formatUTC(windowStart),
		WindowEnd:          formatUTC(startedAt),
		NotificationStatus: "not_needed",
	}
	notificationSettings, notificationErr := m.notificationSettings()

	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	stats, queryErr := m.client.GetDailyReportStats(queryCtx, windowStart, startedAt, report.Timezone)
	cancel()
	if queryErr != nil {
		run.Status = "error"
		run.Error = safeError(queryErr)
		run.Message = "查询每日统计失败"
		m.sendFailure("每日报告", notificationSettings, notificationErr, location, startedAt, queryErr, &run)
		return m.finish(report, run, startedAt)
	}

	summary := DailyReportSummary{
		Date:            localNow.Format("2006-01-02"),
		Timezone:        report.Timezone,
		TotalActualCost: stats.TotalActualCost,
		TotalTokens:     stats.TotalTokens,
		NewUsers:        stats.NewUsers,
		RechargeAmounts: stats.RechargeAmounts,
		RechargeUsers:   stats.RechargeUsers,
	}
	run.Status = "ok"
	run.Message = "每日统计完成"
	run.Summary = summary
	if notificationErr != nil {
		run.NotificationStatus = "failed"
		run.NotificationError = safeError(notificationErr)
		run.Message = "每日统计完成，但通知配置读取失败"
	} else if settings, ok := completeWeComSettings(notificationSettings); ok {
		_, sendErr := m.wecom.Send(ctx, settings, wecom.Text, buildDailyText(summary, startedAt, windowStart, location))
		if sendErr != nil {
			run.NotificationStatus = "failed"
			run.NotificationError = safeError(sendErr)
			run.Message = "每日统计完成，但企微投递失败"
		} else {
			run.NotificationStatus = "sent"
		}
	} else {
		run.NotificationStatus = "disabled"
	}
	return m.finish(report, run, startedAt)
}

func (m *Manager) sendFailure(title string, notificationSettings store.ScheduledReportNotificationSettings, notificationErr error, location *time.Location, startedAt time.Time, queryErr error, run *store.ScheduledReportRun) {
	if notificationErr != nil {
		run.NotificationStatus = "failed"
		run.NotificationError = safeError(notificationErr)
		return
	}
	settings, ok := completeWeComSettings(notificationSettings)
	if !ok {
		run.NotificationStatus = "disabled"
		return
	}
	if _, err := m.wecom.Send(context.Background(), settings, wecom.Text, buildFailureText(title, startedAt, location, queryErr)); err != nil {
		run.NotificationStatus = "failed"
		run.NotificationError = safeError(err)
		return
	}
	run.NotificationStatus = "sent"
}

func (m *Manager) finish(report store.ScheduledReport, run store.ScheduledReportRun, startedAt time.Time) (store.ScheduledReportRun, error) {
	run.FinishedAt = formatUTC(time.Now().UTC())
	saved, err := m.store.AddScheduledReportRun(run)
	if err != nil {
		return store.ScheduledReportRun{}, err
	}
	lastError := run.Error
	if lastError == "" {
		lastError = run.NotificationError
	}
	nextRunAt := nextScheduledAt(report, startedAt)
	if err := m.store.UpdateScheduledReportRunState(report.ID, run.StartedAt, run.Status, lastError, nextRunAt); err != nil {
		return store.ScheduledReportRun{}, err
	}
	return saved, nil
}

func (m *Manager) viewFor(report store.ScheduledReport, config storedConfig) (View, error) {
	if !report.Enabled {
		report.NextRunAt = ""
	} else if report.LastRunAt != "" {
		if lastRunAt, err := time.Parse(time.RFC3339Nano, report.LastRunAt); err == nil {
			report.NextRunAt = nextScheduledAt(report, lastRunAt)
		} else {
			report.NextRunAt = firstEligibleRunAt(time.Now(), report.StartHour, report.EndHour, report.Timezone)
		}
	} else {
		report.NextRunAt = firstEligibleRunAt(time.Now(), report.StartHour, report.EndHour, report.Timezone)
	}
	latest := (*store.ScheduledReportRun)(nil)
	if report.ID > 0 {
		items, _, _, _, err := m.store.ScheduledReportRuns(report.ID, 1, 1)
		if err != nil {
			return View{}, err
		}
		if len(items) > 0 {
			latest = &items[0]
		}
	}
	return View{
		Config: ChannelUsageConfig{
			Enabled: report.Enabled, IntervalMinutes: report.IntervalMinutes,
			StartHour: report.StartHour, EndHour: report.EndHour, Timezone: report.Timezone,
			LookbackHours: config.LookbackHours, FirstTokenThresholdMS: config.FirstTokenThresholdMS,
			TriggerCount: config.TriggerCount,
			LastRunAt:    report.LastRunAt, LastStatus: report.LastStatus,
			LastError: report.LastError, NextRunAt: report.NextRunAt,
		},
		Connection: ConnectionSummary{Configured: m.client.Ready() == nil, BaseURL: m.client.BaseURL()},
		LatestRun:  latest,
	}, nil
}

func (m *Manager) dailyViewFor(report store.ScheduledReport) (DailyView, error) {
	if !report.Enabled {
		report.NextRunAt = ""
	} else if report.LastRunAt != "" {
		if lastRunAt, err := time.Parse(time.RFC3339Nano, report.LastRunAt); err == nil {
			report.NextRunAt = nextScheduledAt(report, lastRunAt)
		} else {
			report.NextRunAt = firstEligibleRunAt(time.Now(), report.StartHour, report.EndHour, report.Timezone)
		}
	} else {
		report.NextRunAt = firstEligibleRunAt(time.Now(), report.StartHour, report.EndHour, report.Timezone)
	}
	latest := (*store.ScheduledReportRun)(nil)
	if report.ID > 0 {
		items, _, _, _, err := m.store.ScheduledReportRuns(report.ID, 1, 1)
		if err != nil {
			return DailyView{}, err
		}
		if len(items) > 0 {
			latest = &items[0]
		}
	}
	return DailyView{
		Config: DailyReportConfig{
			Enabled: report.Enabled, RunHour: report.StartHour, Timezone: report.Timezone,
			LastRunAt: report.LastRunAt, LastStatus: report.LastStatus,
			LastError: report.LastError, NextRunAt: report.NextRunAt,
		},
		Connection: ConnectionSummary{Configured: m.client.Ready() == nil, BaseURL: m.client.BaseURL()},
		LatestRun:  latest,
	}, nil
}

func validateSaveInput(input SaveInput) error {
	if input.IntervalMinutes < 1 || input.IntervalMinutes > maxIntervalMinutes {
		return invalid("运行间隔必须是 1–1440 分钟")
	}
	if input.LookbackHours < 1 || input.LookbackHours > maxLookbackHours {
		return invalid("最近统计小时数必须是 1–168 小时")
	}
	if input.FirstTokenThresholdMS <= 0 {
		return invalid("首 T 延迟阈值必须是正整数")
	}
	if input.TriggerCount <= 0 {
		return invalid("触发告警条数必须是正整数")
	}
	if input.StartHour < 0 || input.StartHour > 23 || input.EndHour < 0 || input.EndHour > 23 || input.StartHour > input.EndHour {
		return invalid("开始小时和结束小时必须在 0–23，且开始不晚于结束")
	}
	if strings.TrimSpace(input.Timezone) == "" {
		return invalid("时区不能为空")
	}
	if _, err := time.LoadLocation(strings.TrimSpace(input.Timezone)); err != nil {
		return invalid("时区无效")
	}
	return nil
}

func validateDailySaveInput(input DailySaveInput) error {
	if input.RunHour < 0 || input.RunHour > 23 {
		return invalid("每日执行小时必须在 0–23")
	}
	if strings.TrimSpace(input.Timezone) == "" {
		return invalid("时区不能为空")
	}
	if _, err := time.LoadLocation(strings.TrimSpace(input.Timezone)); err != nil {
		return invalid("时区无效")
	}
	return nil
}

func validateNotificationSettings(settings store.ScheduledReportNotificationSettings, requireComplete bool) error {
	if err := wecom.Validate(toWeComSettings(settings.WeCom), requireComplete); err != nil {
		return invalid(err.Error())
	}
	return nil
}

func defaultStoredConfig() storedConfig {
	return storedConfig{LookbackHours: defaultLookbackHours, FirstTokenThresholdMS: defaultFirstTokenMS, TriggerCount: defaultTriggerCount}
}

func defaultScheduledReport() store.ScheduledReport {
	config, _ := json.Marshal(defaultStoredConfig())
	return store.ScheduledReport{
		Type:    store.ScheduledReportChannelUsage,
		Enabled: false, IntervalMinutes: defaultIntervalMinutes,
		StartHour: defaultStartHour, EndHour: defaultEndHour, Timezone: defaultTimezone,
		ConfigJSON: string(config), LastStatus: "never",
	}
}

func defaultDailyReport() store.ScheduledReport {
	return store.ScheduledReport{
		Type:    store.ScheduledReportDaily,
		Enabled: false, IntervalMinutes: 24 * 60,
		StartHour: 23, EndHour: 23, Timezone: defaultTimezone,
		ConfigJSON: `{}`, LastStatus: "never",
	}
}

func decodeStoredConfig(raw string) (storedConfig, error) {
	config := defaultStoredConfig()
	if strings.TrimSpace(raw) == "" {
		return config, nil
	}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return storedConfig{}, fmt.Errorf("渠道使用报告配置损坏")
	}
	if config.LookbackHours <= 0 {
		config.LookbackHours = defaultLookbackHours
	}
	if config.FirstTokenThresholdMS <= 0 {
		config.FirstTokenThresholdMS = defaultFirstTokenMS
	}
	if config.TriggerCount <= 0 {
		config.TriggerCount = defaultTriggerCount
	}
	return config, nil
}

func (m *Manager) notificationSettings() (store.ScheduledReportNotificationSettings, error) {
	settings, exists, err := m.store.ScheduledReportNotificationSettings()
	if err != nil {
		return store.ScheduledReportNotificationSettings{}, err
	}
	if exists {
		return settings, nil
	}
	report, exists, err := m.store.ScheduledReport(store.ScheduledReportChannelUsage)
	if err != nil || !exists {
		return settings, err
	}
	legacy, ok, err := legacyNotificationSettings(report.ConfigJSON)
	if err != nil || !ok {
		return settings, err
	}
	saved, err := m.store.SaveScheduledReportNotificationSettings(legacy)
	if err != nil {
		return store.ScheduledReportNotificationSettings{}, err
	}
	if cleaned, changed, err := removeLegacyNotificationSettings(report.ConfigJSON); err == nil && changed {
		report.ConfigJSON = cleaned
		// 共享配置已经落库；清理失败不应阻断通知配置读取，下一次仍可从共享配置继续运行。
		_, _ = m.store.SaveScheduledReportConfig(report)
	}
	return saved, nil
}

func legacyNotificationSettings(raw string) (store.ScheduledReportNotificationSettings, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return store.DefaultScheduledReportNotificationSettings(), false, nil
	}
	var legacy legacyStoredConfig
	if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
		return store.ScheduledReportNotificationSettings{}, false, fmt.Errorf("渠道使用报告配置损坏")
	}
	if legacy.WeCom == nil {
		return store.DefaultScheduledReportNotificationSettings(), false, nil
	}
	return store.ScheduledReportNotificationSettings{
		WeCom: store.ScheduledReportWeComSettings{
			Enabled: legacy.WeCom.Enabled, CorpID: legacy.WeCom.CorpID,
			AgentID: legacy.WeCom.AgentID, Secret: legacy.WeCom.Secret, Target: legacy.WeCom.Target,
		},
	}, true, nil
}

func removeLegacyNotificationSettings(raw string) (string, bool, error) {
	values := make(map[string]json.RawMessage)
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return "", false, err
	}
	if _, exists := values["wecom"]; !exists {
		return raw, false, nil
	}
	delete(values, "wecom")
	cleaned, err := json.Marshal(values)
	if err != nil {
		return "", false, err
	}
	return string(cleaned), true, nil
}

func notificationConfig(settings store.ScheduledReportNotificationSettings) NotificationConfig {
	return NotificationConfig{
		WeCom: NotificationWeComConfig{
			Enabled: settings.WeCom.Enabled, CorpID: settings.WeCom.CorpID,
			AgentID: settings.WeCom.AgentID, Secret: settings.WeCom.Secret, Target: settings.WeCom.Target,
			HasSecret: strings.TrimSpace(settings.WeCom.Secret) != "",
		},
	}
}

func toWeComSettings(settings store.ScheduledReportWeComSettings) wecom.Settings {
	return wecom.Settings{CorpID: settings.CorpID, AgentID: settings.AgentID, Secret: settings.Secret, Target: settings.Target}
}

func completeWeComSettings(settings store.ScheduledReportNotificationSettings) (wecom.Settings, bool) {
	if !settings.WeCom.Enabled || wecom.Validate(toWeComSettings(settings.WeCom), true) != nil {
		return wecom.Settings{}, false
	}
	return toWeComSettings(settings.WeCom), true
}

func withinWindow(now time.Time, startHour, endHour int) bool {
	return now.Hour() >= startHour && now.Hour() <= endHour
}

func firstEligibleRunAt(now time.Time, startHour, endHour int, timezone string) string {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return ""
	}
	local := now.In(location)
	if local.Hour() < startHour {
		local = time.Date(local.Year(), local.Month(), local.Day(), startHour, 0, 0, 0, location)
	} else if local.Hour() > endHour {
		local = time.Date(local.Year(), local.Month(), local.Day()+1, startHour, 0, 0, 0, location)
	}
	return formatUTC(local.UTC())
}

func nextScheduledAt(report store.ScheduledReport, after time.Time) string {
	location, err := time.LoadLocation(report.Timezone)
	if err != nil {
		return ""
	}
	candidate := after.In(location).Add(time.Duration(report.IntervalMinutes) * time.Minute)
	if candidate.Hour() < report.StartHour {
		candidate = time.Date(candidate.Year(), candidate.Month(), candidate.Day(), report.StartHour, 0, 0, 0, location)
	} else if candidate.Hour() > report.EndHour {
		candidate = time.Date(candidate.Year(), candidate.Month(), candidate.Day()+1, report.StartHour, 0, 0, 0, location)
	}
	return formatUTC(candidate.UTC())
}

func formatUTC(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func safeError(err error) string {
	if err == nil {
		return ""
	}
	return truncateText(strings.TrimSpace(err.Error()), 1000)
}
