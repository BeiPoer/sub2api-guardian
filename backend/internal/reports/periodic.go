package reports

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/wecom"
)

func isPeriodicType(reportType store.ScheduledReportType) bool {
	return reportType == store.ScheduledReportDaily || reportType == store.ScheduledReportWeekly
}

func periodicTitle(reportType store.ScheduledReportType) string {
	if reportType == store.ScheduledReportWeekly {
		return "每周报告"
	}
	return "每日报告"
}

func (m *Manager) periodicReports() ([2]store.ScheduledReport, error) {
	var reports [2]store.ScheduledReport
	for i, reportType := range []store.ScheduledReportType{store.ScheduledReportDaily, store.ScheduledReportWeekly} {
		report, exists, err := m.store.ScheduledReport(reportType)
		if err != nil {
			return reports, err
		}
		if !exists {
			report = defaultDailyReport()
			if reportType == store.ScheduledReportWeekly {
				config, err := decodeDailyStoredConfig(reports[0].ConfigJSON)
				if err != nil {
					return reports, err
				}
				report.Type = reportType
				report.IntervalMinutes = 7 * 24 * 60
				report.StartHour, report.EndHour = 9, 9
				report.Timezone = reports[0].Timezone
				raw, _ := json.Marshal(storedDailyConfig{SourceID: config.SourceID, Weekday: 1})
				report.ConfigJSON = string(raw)
			}
		}
		reports[i] = report
	}
	return reports, nil
}

func (m *Manager) PeriodicView() (PeriodicView, error) {
	m.configMu.Lock()
	defer m.configMu.Unlock()
	reports, err := m.periodicReports()
	if err != nil {
		return PeriodicView{}, err
	}
	daily, err := m.dailyViewFor(reports[0])
	if err != nil {
		return PeriodicView{}, err
	}
	weekly, err := m.dailyViewFor(reports[1])
	if err != nil {
		return PeriodicView{}, err
	}
	return PeriodicView{
		SourceID: daily.Config.SourceID, Source: daily.Source, Sources: daily.Sources,
		Daily:  PeriodicReportState{Config: daily.Config, LatestRun: daily.LatestRun},
		Weekly: PeriodicReportState{Config: weekly.Config, LatestRun: weekly.LatestRun},
	}, nil
}

func (m *Manager) SavePeriodic(input PeriodicSaveInput) (PeriodicView, error) {
	if err := m.savePeriodic(input); err != nil {
		return PeriodicView{}, err
	}
	return m.PeriodicView()
}

func (m *Manager) savePeriodic(input PeriodicSaveInput) error {
	m.configMu.Lock()
	defer m.configMu.Unlock()
	reports, err := m.periodicReports()
	if err != nil {
		return err
	}
	return m.savePeriodicReports(reports, input.SourceID, [2]PeriodicScheduleInput{input.Daily, input.Weekly})
}

func (m *Manager) saveLegacyDaily(input DailySaveInput) error {
	m.configMu.Lock()
	defer m.configMu.Unlock()
	reports, err := m.periodicReports()
	if err != nil {
		return err
	}
	var schedules [2]PeriodicScheduleInput
	for i, report := range reports {
		config, err := decodeDailyStoredConfig(report.ConfigJSON)
		if err != nil {
			return err
		}
		schedules[i] = PeriodicScheduleInput{
			Enabled: report.Enabled, RunHour: report.StartHour, Timezone: report.Timezone,
			WeComTarget: config.WeComTarget, Weekday: config.Weekday,
		}
	}
	schedules[0].Enabled, schedules[0].RunHour, schedules[0].Timezone = input.Enabled, input.RunHour, input.Timezone
	if input.WeComTarget != nil {
		schedules[0].WeComTarget = *input.WeComTarget
	}
	if reports[1].ID == 0 {
		schedules[1].Timezone = input.Timezone
	}
	return m.savePeriodicReports(reports, input.SourceID, schedules)
}

func (m *Manager) savePeriodicReports(reports [2]store.ScheduledReport, sourceID string, schedules [2]PeriodicScheduleInput) error {
	if _, err := m.notificationSettings(); err != nil {
		return err
	}
	if strings.TrimSpace(sourceID) == "" {
		config, err := decodeDailyStoredConfig(reports[0].ConfigJSON)
		if err != nil {
			return err
		}
		sourceID = config.SourceID
	}
	sourceID, err := m.validateSourceID(sourceID)
	if err != nil {
		return err
	}
	for i, schedule := range schedules {
		if err := validateDailySaveInput(DailySaveInput{RunHour: schedule.RunHour, Timezone: schedule.Timezone}); err != nil {
			return err
		}
		if err := validateReportTarget(schedule.WeComTarget); err != nil {
			return err
		}
		if i == 1 && (schedule.Weekday < 1 || schedule.Weekday > 7) {
			return invalid("执行星期必须在 1–7（周一至周日）")
		}
		if i == 0 {
			schedule.Weekday = 0
		}
		raw, err := json.Marshal(storedDailyConfig{
			SourceID: sourceID, WeComTarget: strings.TrimSpace(schedule.WeComTarget), Weekday: schedule.Weekday,
		})
		if err != nil {
			return err
		}
		reports[i].Enabled = schedule.Enabled
		reports[i].StartHour, reports[i].EndHour = schedule.RunHour, schedule.RunHour
		reports[i].Timezone = strings.TrimSpace(schedule.Timezone)
		reports[i].ConfigJSON = string(raw)
	}
	return m.store.SaveScheduledReportConfigs(reports[:]...)
}

func (m *Manager) PeriodicRuns(reportType store.ScheduledReportType, page, pageSize int) ([]store.ScheduledReportRun, int64, int, int, int, error) {
	if !isPeriodicType(reportType) {
		return nil, 0, 0, 0, 0, invalid("周期报告类型必须是 daily 或 weekly")
	}
	return m.runsFor(reportType, page, pageSize)
}

func (m *Manager) RunPeriodicNow(ctx context.Context, reportType store.ScheduledReportType) (store.ScheduledReportRun, error) {
	if !isPeriodicType(reportType) {
		return store.ScheduledReportRun{}, invalid("周期报告类型必须是 daily 或 weekly")
	}
	if !m.runMu.TryLock() {
		return store.ScheduledReportRun{}, ErrAlreadyRunning
	}
	defer m.runMu.Unlock()
	report, err := m.ensurePeriodicReport(reportType)
	if err != nil {
		return store.ScheduledReportRun{}, err
	}
	return m.executeDaily(ctx, report)
}

func (m *Manager) ensurePeriodicReport(reportType store.ScheduledReportType) (store.ScheduledReport, error) {
	m.configMu.Lock()
	defer m.configMu.Unlock()
	reports, err := m.periodicReports()
	if err != nil {
		return store.ScheduledReport{}, err
	}
	if reports[0].ID == 0 || reports[1].ID == 0 {
		if err := m.store.SaveScheduledReportConfigs(reports[:]...); err != nil {
			return store.ScheduledReport{}, err
		}
	}
	report, _, err := m.store.ScheduledReport(reportType)
	return report, err
}

func validateReportTarget(target string) error {
	if err := wecom.Validate(wecom.Settings{Target: target}, false); err != nil {
		return invalid(err.Error())
	}
	return nil
}

func (m *Manager) reportNotificationSettings(target string) (store.ScheduledReportNotificationSettings, error) {
	settings, err := m.notificationSettings()
	if target = strings.TrimSpace(target); target != "" {
		settings.WeCom.Target = target
	}
	return settings, err
}

func periodicWindowStart(reportType store.ScheduledReportType, localNow time.Time) time.Time {
	start := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, localNow.Location())
	if reportType == store.ScheduledReportWeekly {
		start = start.AddDate(0, 0, -(int(localNow.Weekday())+6)%7)
	}
	return start
}

// nextPeriodicRun 同时供调度和页面使用；在尚未执行的计划小时内返回 now。
func nextPeriodicRun(report store.ScheduledReport, config storedDailyConfig, now time.Time) time.Time {
	if !report.Enabled || !isPeriodicType(report.Type) {
		return time.Time{}
	}
	location, err := time.LoadLocation(report.Timezone)
	if err != nil {
		return time.Time{}
	}
	localNow := now.In(location)
	last, _ := time.Parse(time.RFC3339Nano, report.LastRunAt)
	last = last.In(location)
	for offset := 0; offset <= 14; offset++ {
		day := time.Date(localNow.Year(), localNow.Month(), localNow.Day()+offset, 12, 0, 0, 0, location)
		weekday := (int(day.Weekday())+6)%7 + 1
		if report.Type == store.ScheduledReportWeekly && weekday != config.Weekday {
			continue
		}
		// 同一当地日期的重复 DST 小时也只能执行一次，手动执行在该小时内同样计入。
		if last.Year() == day.Year() && last.YearDay() == day.YearDay() && last.Hour() == report.StartHour {
			continue
		}
		if offset == 0 && localNow.Hour() == report.StartHour {
			return now
		}
		candidate := time.Date(day.Year(), day.Month(), day.Day(), report.StartHour, 0, 0, 0, location)
		// time.Date 可能选择回拨后的第二个小时；预计时间应指向最早的那个。
		if zoneStart, _ := candidate.ZoneBounds(); !zoneStart.IsZero() {
			_, previousOffset := zoneStart.Add(-time.Nanosecond).Zone()
			_, offset := candidate.Zone()
			if previousOffset > offset {
				earlier := candidate.Add(-time.Duration(previousOffset-offset) * time.Second)
				if earlier.Year() == day.Year() && earlier.YearDay() == day.YearDay() && earlier.Hour() == report.StartHour {
					candidate = earlier
				}
			}
		}
		// 跳过夏令时切换中不存在的小时。
		if candidate.Hour() != report.StartHour || candidate.Day() != day.Day() || candidate.Before(now) {
			continue
		}
		return candidate
	}
	return time.Time{}
}

func periodicNextRunAt(report store.ScheduledReport, config storedDailyConfig, now time.Time) string {
	if next := nextPeriodicRun(report, config, now); !next.IsZero() {
		return formatUTC(next)
	}
	return ""
}
