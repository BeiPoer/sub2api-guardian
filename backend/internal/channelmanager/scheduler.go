package channelmanager

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/store"
)

const upstreamGroupStateKey = "groups"

type EvaluationResult struct {
	Triggered bool   `json:"triggered"`
	Message   string `json:"message"`
	Snapshot  any    `json:"snapshot,omitempty"`
}

func EvaluateBalanceTask(task store.UpstreamAutomationTask, snapshots []store.UpstreamBalanceSnapshot, channelName string) EvaluationResult {
	if len(snapshots) == 0 {
		return EvaluationResult{Message: "缺少余额快照"}
	}
	latest := snapshots[len(snapshots)-1]
	if task.Type == store.UpstreamTaskLowBalance {
		triggered := latest.Balance <= task.Threshold
		message := channelName + " 当前余额未低于阈值"
		if triggered {
			message = fmt.Sprintf("%s 当前余额 %.2f，已低于或等于阈值 %.2f", channelName, latest.Balance, task.Threshold)
		}
		return EvaluationResult{Triggered: triggered, Message: message, Snapshot: latest}
	}
	if task.Type != store.UpstreamTaskBurnRate {
		return EvaluationResult{Message: "任务类型不是余额任务"}
	}
	latestTime := parseStoredTime(latest.CapturedAt)
	cutoff := latestTime.Add(-time.Duration(task.LookbackMinutes) * time.Minute)
	oldest := snapshots[0]
	for _, snapshot := range snapshots {
		if !parseStoredTime(snapshot.CapturedAt).Before(cutoff) {
			oldest = snapshot
			break
		}
	}
	if oldest.ID == latest.ID {
		return EvaluationResult{Message: "窗口内余额快照不足", Snapshot: latest}
	}
	consumed := oldest.Balance - latest.Balance
	if consumed <= 0 {
		return EvaluationResult{Message: channelName + " 余额上涨或未消耗，不触发消耗过快预警", Snapshot: map[string]any{
			"old": oldest, "latest": latest, "consumed": consumed,
		}}
	}
	elapsedMinutes := parseStoredTime(latest.CapturedAt).Sub(parseStoredTime(oldest.CapturedAt)).Minutes()
	if elapsedMinutes < 1 {
		elapsedMinutes = 1
	}
	hourlyRate := consumed / (elapsedMinutes / 60)
	triggered := hourlyRate >= task.Threshold
	message := channelName + " 消耗速度未超过阈值"
	if triggered {
		message = fmt.Sprintf("%s 最近 %.0f 分钟消耗 %.2f，折算每小时 %.2f，超过阈值 %.2f",
			channelName, elapsedMinutes, consumed, hourlyRate, task.Threshold)
	}
	return EvaluationResult{Triggered: triggered, Message: message, Snapshot: map[string]any{
		"old": oldest, "latest": latest, "consumed": consumed, "hourly_rate": hourlyRate,
	}}
}

func EvaluateGroupTask(task store.UpstreamAutomationTask, beforeGroups, afterGroups any, channelName string, hasBaseline bool) EvaluationResult {
	before := normalizeGroupsForMonitoring(beforeGroups)
	after := normalizeGroupsForMonitoring(afterGroups)
	if !hasBaseline {
		return EvaluationResult{Message: channelName + " 缺少历史分组缓存，本次仅建立基线", Snapshot: map[string]any{
			"before": []any{}, "after": groupInfoValues(after),
		}}
	}
	added := make([]groupInfo, 0)
	removed := make([]groupInfo, 0)
	changed := make([]map[string]any, 0)
	for key, group := range after {
		old, exists := before[key]
		if !exists {
			added = append(added, group)
			continue
		}
		if old.Ratio != nil && group.Ratio != nil && *old.Ratio != *group.Ratio {
			changed = append(changed, map[string]any{"key": key, "label": group.Label, "before": *old.Ratio, "after": *group.Ratio})
		}
	}
	for key, group := range before {
		if _, exists := after[key]; !exists {
			removed = append(removed, group)
		}
	}
	sort.Slice(added, func(i, j int) bool { return added[i].Label < added[j].Label })
	sort.Slice(removed, func(i, j int) bool { return removed[i].Label < removed[j].Label })
	sort.Slice(changed, func(i, j int) bool { return stringValue(changed[i]["label"]) < stringValue(changed[j]["label"]) })

	switch task.Type {
	case store.UpstreamTaskGroupAdded:
		message := channelName + " 未发现新增分组"
		if len(added) > 0 {
			message = channelName + " 新增分组：" + groupLabels(added)
		}
		return EvaluationResult{Triggered: len(added) > 0, Message: message, Snapshot: map[string]any{"added": added, "before": groupInfoValues(before), "after": groupInfoValues(after)}}
	case store.UpstreamTaskGroupRemoved:
		message := channelName + " 未发现减少分组"
		if len(removed) > 0 {
			message = channelName + " 减少分组：" + groupLabels(removed)
		}
		return EvaluationResult{Triggered: len(removed) > 0, Message: message, Snapshot: map[string]any{"removed": removed, "before": groupInfoValues(before), "after": groupInfoValues(after)}}
	default:
		message := channelName + " 未发现分组倍率变化"
		if len(changed) > 0 {
			parts := make([]string, 0, len(changed))
			for _, item := range changed {
				parts = append(parts, fmt.Sprintf("%s %v -> %v", item["label"], item["before"], item["after"]))
			}
			message = channelName + " 分组倍率变化：" + strings.Join(parts, "、")
		}
		return EvaluationResult{Triggered: len(changed) > 0, Message: message, Snapshot: map[string]any{"changed": changed, "before": groupInfoValues(before), "after": groupInfoValues(after)}}
	}
}

func (m *Manager) RunDueTasks(ctx context.Context) error { return m.runDueTasks(ctx) }

func (m *Manager) runDueTasks(ctx context.Context) error {
	channels, err := m.store.UpstreamChannels()
	if err != nil {
		return err
	}
	now := time.Now()
	var firstErr error
	for _, channel := range channels {
		if channel.Ignored || channel.Type == store.UpstreamChannelOther {
			continue
		}
		tasks, err := m.store.UpstreamAutomationTasks(channel.ID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		due := make([]store.UpstreamAutomationTask, 0)
		for _, task := range tasks {
			if task.Enabled && minutesSince(task.LastRunAt, now) >= float64(task.IntervalMinutes) {
				due = append(due, task)
			}
		}
		if len(due) == 0 {
			continue
		}
		if err := m.runChannelTasks(ctx, channel, due, now); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *Manager) runChannelTasks(ctx context.Context, channel store.UpstreamChannel, tasks []store.UpstreamAutomationTask, now time.Time) error {
	return m.withChannelLock(ctx, channel.ID, func() error {
		for _, task := range tasks {
			if err := m.store.MarkUpstreamTaskRun(task.ID, now); err != nil {
				return err
			}
		}
		beforeGroups, err := m.store.UpstreamCache(channel.ID, "groups")
		if err != nil {
			return err
		}
		beforeTokens, err := m.store.UpstreamCache(channel.ID, "tokens")
		if err != nil {
			return err
		}
		hasGroupTask := false
		maxLookback := 1
		for _, task := range tasks {
			hasGroupTask = hasGroupTask || task.Type.IsGroupTask()
			maxLookback = max(maxLookback, task.LookbackMinutes)
		}
		if hasGroupTask {
			if err := m.syncLocked(ctx, channel.ID); err != nil {
				return err
			}
		} else if _, err := m.balanceLocked(ctx, channel.ID); err != nil {
			return err
		}

		afterGroups, err := m.store.UpstreamCache(channel.ID, "groups")
		if err != nil {
			return err
		}
		afterTokens, err := m.store.UpstreamCache(channel.ID, "tokens")
		if err != nil {
			return err
		}
		snapshots, err := m.store.UpstreamBalanceSnapshotsSince(channel.ID, now.Add(-time.Duration(maxLookback)*time.Minute))
		if err != nil {
			return err
		}
		for _, task := range tasks {
			var evaluation EvaluationResult
			if task.Type.IsGroupTask() {
				previous, exists, err := m.store.UpstreamTaskState(task.ID, upstreamGroupStateKey)
				if err != nil {
					return err
				}
				before := previous
				hasBaseline := exists
				if !exists && beforeGroups.Exists {
					before = beforeGroups.Value
					hasBaseline = true
				}
				after := afterGroups.Value
				state := after
				if task.Type == store.UpstreamTaskGroupRatioChange {
					if !exists {
						before = filterGroupsByTokenUsage(before, beforeTokens.Value, channel.Type)
					}
					after = filterGroupsByTokenUsage(after, afterTokens.Value, channel.Type)
					state = after
				}
				evaluation = EvaluateGroupTask(task, before, after, channel.Name, hasBaseline)
				if err := m.store.SaveUpstreamTaskState(task.ID, upstreamGroupStateKey, state); err != nil {
					return err
				}
			} else {
				cutoff := now.Add(-time.Duration(task.LookbackMinutes) * time.Minute)
				taskSnapshots := make([]store.UpstreamBalanceSnapshot, 0)
				for _, snapshot := range snapshots {
					if !parseStoredTime(snapshot.CapturedAt).Before(cutoff) {
						taskSnapshots = append(taskSnapshots, snapshot)
					}
				}
				if len(taskSnapshots) == 0 {
					if latest, err := m.store.LatestUpstreamBalanceSnapshot(channel.ID); err == nil && latest != nil {
						taskSnapshots = append(taskSnapshots, *latest)
					}
				}
				evaluation = EvaluateBalanceTask(task, taskSnapshots, channel.Name)
			}
			if err := m.recordAlert(ctx, channel, task, evaluation, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (m *Manager) recordAlert(ctx context.Context, channel store.UpstreamChannel, task store.UpstreamAutomationTask, evaluation EvaluationResult, now time.Time) error {
	if !evaluation.Triggered || minutesSince(task.LastAlertAt, now) < float64(task.CooldownMinutes) {
		return nil
	}
	subject := "AI 渠道余额预警"
	if task.Type.IsGroupTask() {
		subject = "AI 渠道分组预警"
	}
	emailSent := false
	emailError := ""
	if _, err := m.sendEmail(ctx, task.Recipients, subject, evaluation.Message); err != nil {
		emailError = err.Error()
	} else {
		emailSent = true
	}
	wecomSent := false
	wecomError := ""
	if _, err := m.sendWeCom(ctx, "", subject+"\n"+evaluation.Message); err != nil {
		wecomError = err.Error()
	} else {
		wecomSent = true
	}
	if err := m.store.AddUpstreamAlertEvent(store.UpstreamAlertEvent{
		ChannelID: channel.ID, TaskID: &task.ID, Type: string(task.Type), Message: evaluation.Message,
		Snapshot: evaluation.Snapshot, EmailSent: emailSent, EmailError: emailError,
		WeComSent: wecomSent, WeComError: wecomError, CreatedAt: now.Format(time.RFC3339Nano),
	}); err != nil {
		return err
	}
	return m.store.MarkUpstreamTaskAlert(task.ID, now)
}

// SeedTaskState 在创建或重新启用分组任务时记录当前缓存，防止首次运行误报。
func (m *Manager) SeedTaskState(task store.UpstreamAutomationTask) error {
	if !task.Type.IsGroupTask() {
		return nil
	}
	groups, err := m.store.UpstreamCache(task.ChannelID, "groups")
	if err != nil || !groups.Exists {
		return err
	}
	value := groups.Value
	if task.Type == store.UpstreamTaskGroupRatioChange {
		tokens, err := m.store.UpstreamCache(task.ChannelID, "tokens")
		if err != nil {
			return err
		}
		value = filterGroupsByTokenUsage(value, tokens.Value, taskChannelType(m.store, task.ChannelID))
	}
	return m.store.SaveUpstreamTaskState(task.ID, upstreamGroupStateKey, value)
}

func taskChannelType(st *store.Store, channelID int64) store.UpstreamChannelType {
	channel, err := st.UpstreamChannel(channelID)
	if err != nil {
		return store.UpstreamChannelOther
	}
	return channel.Type
}

func minutesSince(value string, now time.Time) float64 {
	if value == "" {
		return 1e12
	}
	parsed := parseStoredTime(value)
	if parsed.IsZero() {
		return 1e12
	}
	return now.Sub(parsed).Minutes()
}

func parseStoredTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	if parsed.IsZero() {
		parsed, _ = time.Parse(time.RFC3339, value)
	}
	return parsed
}

type groupInfo struct {
	Key   string   `json:"key"`
	Label string   `json:"label"`
	Ratio *float64 `json:"ratio"`
	Raw   any      `json:"raw"`
}

func normalizeGroupsForMonitoring(groups any) map[string]groupInfo {
	out := make(map[string]groupInfo)
	for _, item := range normalizeCollection(groups) {
		key := groupKey(item)
		if key == "" {
			continue
		}
		var ratio *float64
		if value, ok := groupRatio(item); ok {
			ratio = &value
		}
		out[key] = groupInfo{Key: key, Label: key, Ratio: ratio, Raw: item}
	}
	return out
}

func groupKey(group any) string {
	record, ok := asObject(group)
	if !ok {
		return strings.TrimSpace(stringValue(group))
	}
	for _, key := range []string{"name", "group", "key", "code", "id", "group_id", "group_name", "display_name"} {
		if value := strings.TrimSpace(stringValue(record[key])); value != "" {
			return value
		}
	}
	return strings.TrimSpace(stringValue(group))
}

func groupRatio(group any) (float64, bool) {
	if value, ok := finiteNumber(group); ok && value > 0 {
		return value, true
	}
	record, ok := asObject(group)
	if !ok {
		return 0, false
	}
	for _, key := range []string{"user_rate_multiplier", "userRateMultiplier", "custom_rate_multiplier", "customRateMultiplier", "ratio", "rate", "multiplier", "rate_multiplier", "rateMultiplier", "group_ratio", "model_ratio", "倍率", "value"} {
		if value, ok := finiteNumber(record[key]); ok && value > 0 {
			return value, true
		}
	}
	return 0, false
}

// tokenMultipliersForLink 提取完整令牌 Key 对应的用户分组倍率。
// Sub2API 和 new-api 都使用完整 API Key 参与渠道池联动。
func tokenMultipliersForLink(groups, tokens any, channelType store.UpstreamChannelType) map[string]float64 {
	return tokenMultiplierLinkCandidates(groups, tokens, channelType).ratios
}

// TokenMultiplierLinkCandidates returns ratios and completeness diagnostics
// from the shared linker parser.
type TokenMultiplierLinkCandidates struct {
	Ratios     map[string]float64
	Conflicts  int
	Incomplete bool
}

func TokenMultiplierLinkCandidatesForLink(groups, tokens any, channelType store.UpstreamChannelType) TokenMultiplierLinkCandidates {
	extraction := tokenMultiplierLinkCandidates(groups, tokens, channelType)
	return TokenMultiplierLinkCandidates{
		Ratios: extraction.ratios, Conflicts: extraction.conflicts, Incomplete: extraction.incomplete,
	}
}

type tokenMultiplierLinkExtraction struct {
	ratios     map[string]float64
	conflicts  int
	incomplete bool
}

func tokenMultiplierLinkCandidates(groups, tokens any, channelType store.UpstreamChannelType) tokenMultiplierLinkExtraction {
	result := make(map[string]float64)
	if channelType != store.UpstreamChannelSub2API && channelType != store.UpstreamChannelNewAPI {
		return tokenMultiplierLinkExtraction{ratios: result}
	}
	conflicts := make(map[string]struct{})
	incomplete := false
	groupRows := normalizeCollection(groups)
	for _, rawToken := range normalizeCollection(tokens) {
		token, ok := asObject(rawToken)
		if !ok {
			incomplete = true
			continue
		}
		key := tokenKey(token)
		if key == "" {
			// 脱敏或缺失 Key 无法参与精确匹配；快照不能宣称完整。
			incomplete = true
			continue
		}
		if _, conflicted := conflicts[key]; conflicted {
			continue
		}
		var matched any
		identifiers := tokenGroupIdentifiers(token, channelType)
		for _, group := range groupRows {
			for identifier := range groupIdentifiers(group) {
				if identifiers[identifier] {
					matched = group
					break
				}
			}
			if matched != nil {
				break
			}
		}
		if matched == nil {
			// 允许令牌自带完整分组对象作为关联依据；只有名称/Key
			// 没有任何分组信息时才视为缺少分组并跳过。
			embedded := first(token, "group", "Group")
			if len(groupIdentifiers(embedded)) > 0 {
				matched = embedded
			}
		}
		if matched == nil {
			incomplete = true
			continue
		}
		ratio, ok := tokenGroupRatioForLink(matched, token)
		if !ok || ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
			incomplete = true
			continue
		}
		if previous, exists := result[key]; exists && previous != ratio {
			delete(result, key)
			conflicts[key] = struct{}{}
			continue
		}
		result[key] = ratio
	}
	return tokenMultiplierLinkExtraction{ratios: result, conflicts: len(conflicts), incomplete: incomplete}
}

func tokenGroupRatioForLink(group any, token map[string]any) (float64, bool) {
	embedded := first(token, "group", "Group")
	for _, candidate := range []any{group, embedded, token} {
		if value, ok := explicitGroupRatio(candidate); ok {
			return value, true
		}
	}
	for _, candidate := range []any{embedded, group, token} {
		if value, ok := ordinaryGroupRatio(candidate); ok {
			return value, true
		}
	}
	return 0, false
}

func explicitGroupRatio(group any) (float64, bool) {
	record, ok := asObject(group)
	if !ok {
		return 0, false
	}
	for _, key := range []string{"user_rate_multiplier", "userRateMultiplier", "custom_rate_multiplier", "customRateMultiplier"} {
		if value, ok := finiteNumber(record[key]); ok && value > 0 {
			return value, true
		}
	}
	return 0, false
}

func ordinaryGroupRatio(group any) (float64, bool) {
	if value, ok := finiteNumber(group); ok && value > 0 {
		return value, true
	}
	record, ok := asObject(group)
	if !ok {
		return 0, false
	}
	for _, key := range []string{"ratio", "rate", "multiplier", "rate_multiplier", "rateMultiplier", "group_ratio", "model_ratio", "倍率", "value"} {
		if value, ok := finiteNumber(record[key]); ok && value > 0 {
			return value, true
		}
	}
	return 0, false
}

func groupInfoValues(groups map[string]groupInfo) []groupInfo {
	values := make([]groupInfo, 0, len(groups))
	for _, group := range groups {
		values = append(values, group)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Label < values[j].Label })
	return values
}

func groupLabels(groups []groupInfo) string {
	labels := make([]string, 0, len(groups))
	for _, group := range groups {
		label := group.Label
		if group.Ratio != nil {
			label += fmt.Sprintf("(%v)", *group.Ratio)
		}
		labels = append(labels, label)
	}
	return strings.Join(labels, "、")
}

func filterGroupsByTokenUsage(groups, tokens any, channelType store.UpstreamChannelType) []any {
	identifiers := make(map[string]bool)
	for _, token := range normalizeCollection(tokens) {
		for identifier := range tokenGroupIdentifiers(token, channelType) {
			identifiers[identifier] = true
		}
	}
	if len(identifiers) == 0 {
		return []any{}
	}
	for _, group := range normalizeCollection(groups) {
		groupIDs := groupIdentifiers(group)
		matched := false
		for identifier := range groupIDs {
			if identifiers[identifier] {
				matched = true
				break
			}
		}
		if matched {
			for identifier := range groupIDs {
				identifiers[identifier] = true
			}
		}
	}
	filtered := make([]any, 0)
	for _, group := range normalizeCollection(groups) {
		for identifier := range groupIdentifiers(group) {
			if identifiers[identifier] {
				filtered = append(filtered, group)
				break
			}
		}
	}
	return filtered
}

func groupIdentifiers(group any) map[string]bool {
	identifiers := make(map[string]bool)
	record, ok := asObject(group)
	if !ok {
		if text := strings.TrimSpace(stringValue(group)); text != "" {
			identifiers[text] = true
		}
		return identifiers
	}
	for _, key := range []string{"id", "ID", "group_id", "groupId", "groupID", "name", "Name", "group", "Group", "key", "code", "group_name", "groupName", "display_name", "displayName", "title"} {
		value := record[key]
		if nested, ok := asObject(value); ok {
			for identifier := range groupIdentifiers(nested) {
				identifiers[identifier] = true
			}
		} else if text := strings.TrimSpace(stringValue(value)); text != "" {
			identifiers[text] = true
		}
	}
	return identifiers
}

func tokenGroupIdentifiers(token any, channelType store.UpstreamChannelType) map[string]bool {
	identifiers := make(map[string]bool)
	record, ok := asObject(token)
	if !ok {
		return identifiers
	}
	for _, key := range []string{"group_id", "groupId", "groupID", "group_name", "groupName"} {
		if text := strings.TrimSpace(stringValue(record[key])); text != "" {
			identifiers[text] = true
		}
	}
	group := first(record, "group", "Group")
	if nested, ok := asObject(group); ok {
		for identifier := range groupIdentifiers(nested) {
			identifiers[identifier] = true
		}
	} else {
		text := strings.TrimSpace(stringValue(group))
		if text != "" {
			identifiers[text] = true
		} else if channelType == store.UpstreamChannelNewAPI && group != nil {
			identifiers["default"] = true
		}
	}
	return identifiers
}
