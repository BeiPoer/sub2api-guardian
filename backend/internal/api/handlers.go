package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
)

// recentSampleLimit 是渠道列表里「最近结果小方块」的数量。
const recentSampleLimit = 10

// view 是一次读取组装出的完整视图，供 overview / groups / channels 复用。
type view struct {
	global    policy.Policy
	overrides map[int64]*policy.GroupOverride
	groups    []domain.Group
	channels  []ChannelDTO
	states    map[int64]domain.GroupState
}

func (s *Server) buildView(withSamples bool) (view, error) {
	var v view

	// 上游的限流 / 临时不可调度 / 过载窗口都是「到点自动失效」的，
	// 判定必须落在同一个时刻上：逐个渠道各取一次 time.Now() 的话，
	// 恰好卡在窗口边界的那一批会出现前后不一致的归类。
	now := time.Now()

	global, err := s.store.Policy()
	if err != nil {
		return v, err
	}
	overrides, err := s.store.GroupOverrides()
	if err != nil {
		return v, err
	}
	groups, err := s.store.Groups()
	if err != nil {
		return v, err
	}
	accounts, err := s.store.Accounts()
	if err != nil {
		return v, err
	}
	channelStates, err := s.store.ChannelStateMap()
	if err != nil {
		return v, err
	}
	groupStates, err := s.store.GroupStates()
	if err != nil {
		return v, err
	}

	v.global = global
	v.overrides = overrides
	v.groups = groups
	v.states = make(map[int64]domain.GroupState, len(groupStates))
	for _, state := range groupStates {
		v.states[state.GroupID] = state
	}

	groupNames := make(map[int64]string, len(groups))
	for _, group := range groups {
		groupNames[group.ID] = group.Name
	}

	for _, account := range accounts {
		// 所属分组全部被排除的渠道彻底不展示：它已脱离调度系统的管辖范围，
		// 出现在渠道池只会让人困惑「我没排除它，为什么显示已排除」。
		//
		// 注意与渠道级排除的区别：那是人工摘掉单个渠道，必须保持可见，
		// 否则用户无从取消排除。
		if global.AllGroupsExcluded(account.GroupIDSet()) {
			continue
		}

		state := channelStates[account.ID]
		blockKind, blockReason := account.UpstreamBlock(now)
		dto := ChannelDTO{
			ID:                 account.ID,
			Name:               account.Name,
			Platform:           account.Platform,
			Type:               account.Type,
			Status:             account.Status,
			Schedulable:        account.Schedulable,
			UpstreamBlock:      string(blockKind),
			UpstreamBlockText:  blockReason,
			Excluded:           global.AccountExcluded(account.ID),
			Paused:             global.AccountPaused(account.ID),
			Health:             string(effectiveHealth(global, account, state)),
			DesiredHealth:      string(state.DesiredHealth),
			ApplyPending:       applyPending(global, account, state),
			ApplyError:         state.LastApplyError,
			HealthScore:        state.HealthScore,
			ShortScore:         state.ShortScore,
			LongScore:          state.LongScore,
			SampleCount:        state.SampleCount,
			FailStreak:         state.ConsecutiveFail,
			OKStreak:           state.ConsecutiveOK,
			TTFBP50Ms:          state.TTFBP50Ms,
			TTFBP95Ms:          state.TTFBP95Ms,
			Multiplier:         global.MultiplierFor(account.ID, account.Type),
			MultiplierManual:   global.HasManualMultiplier(account.ID),
			Balance:            state.Balance,
			RateMultiplier:     account.RateMultiplier,
			Priority:           account.Priority,
			LoadFactor:         account.LoadFactor,
			Concurrency:        account.Concurrency,
			Weight:             state.Weight,
			DesiredPriority:    state.DesiredPriority,
			DesiredLoadFactor:  state.DesiredLoadFactor,
			DesiredConcurrency: state.DesiredConcurrency,
			FusedReason:        state.FusedReason,
			FusedUntil:         state.FusedUntil,
			CooldownTill:       state.CooldownTill,
			LastSampleAt:       state.LastSampleAt,
			LastProbeAt:        state.LastProbeAt,
			LastError:          firstNonEmpty(state.LastError, account.ErrorMessage),
			TestModel:          global.AccountTestModels[strconv.FormatInt(account.ID, 10)],
			Models:             state.Models,
			LastRequestModel:   state.LastRequestModel,
			LastProbeModel:     state.LastProbeModel,
			ModelRewritten:     state.ModelRewritten,
			PrimaryGrp:         state.GroupID,
		}

		// 同 Channels：宁可回 [] 也不要 null，前端 .length 会直接抛异常白屏。
		dto.Groups = []GroupRef{}
		managedGroups := 0
		for _, groupID := range account.GroupIDSet() {
			name, ok := groupNames[groupID]
			if !ok {
				continue
			}
			dto.Groups = append(dto.Groups, GroupRef{ID: groupID, Name: name})
			if global.GroupEnabled(groupID, overrides[groupID]) {
				managedGroups++
			}
		}

		// Managed 只表示「在守护范围内」，不含排除状态。
		//
		// 早期版本把 Excluded 也算进来，导致渠道池的「仅受管」筛选一勾选，
		// 被排除的渠道就彻底消失、无从恢复。排除是独立维度，前端自己决定要不要显示。
		dto.Managed = managedGroups > 0 &&
			global.TypeManaged(account.Type) && global.PlatformManaged(account.Platform)

		if withSamples {
			samples, err := s.store.RecentSamples(account.ID, recentSampleLimit)
			if err == nil {
				dto.Recent = toSampleDTOs(samples)
			}
		}
		v.channels = append(v.channels, dto)
	}
	return v, nil
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	v, err := s.buildView(true)
	if err != nil {
		writeError(w, err)
		return
	}
	events, _, err := s.store.Events(store2EventFilter(1, 20))
	if err != nil {
		writeError(w, err)
		return
	}

	status := s.engine.Status()
	out := OverviewDTO{
		Status:            status,
		Events:            events,
		ConcurrencyLimit:  v.global.Scaling.GlobalMaxConcurrency,
		MonitoringEnabled: status.MonitoringEnabled,
		Groups:            buildGroupDTOs(v),
	}

	var scoreSum float64
	var scored int
	for _, ch := range v.channels {
		// 排除的渠道不参与调度，也不该出现在总览统计里。
		if !ch.Managed || ch.Excluded {
			continue
		}
		out.TotalChannels++
		out.AllocatedConc += ch.Concurrency

		health := domain.ChannelHealth(ch.Health)
		// 上游此刻不发流量的渠道不算「健康」，无论它探测得多好。
		//
		// 与分组聚合保持同一口径（见 engine/groups.go）：探测满分但接不到
		// 一个请求的渠道，计入健康数会让面板与网站对不上。原因不止「关掉调用」
		// 一种 —— 限流窗口、临时不可调度、过载退避都会让上游跳过它。
		//
		// 熔断 / 保底本来就该是不可调度的，它们有各自的计数，不在这里改判。
		if ch.UpstreamBlock != "" {
			switch health {
			case domain.HealthHealthy, domain.HealthUnknown, domain.HealthDegraded:
				if ch.UpstreamBlock == string(domain.BlockRateLimited) {
					// 限流的渠道仍在池子里、到点自动恢复，归到「已关闭」会让人
					// 以为需要动手。按降级计，与分组矩阵一致。
					out.DegradedChannels++
					out.RateLimitedChannels++
				} else {
					out.UnschedulableChannels++
				}
				continue
			}
		}

		switch health {
		case domain.HealthHealthy:
			out.HealthyChannels++
		case domain.HealthUnknown:
			// 待探测独立统计：它在 sub2api 侧正常服务，只是还没采到样本，
			// 混进降级会让刚同步完的面板一片「异常」。
			out.PendingChannels++
		case domain.HealthDegraded:
			out.DegradedChannels++
		case domain.HealthFused:
			out.FusedChannels++
		case domain.HealthSurvivor:
			out.SurvivorChannels++
		}
		if ch.SampleCount > 0 {
			scoreSum += ch.HealthScore
			scored++
		}
	}
	if scored > 0 {
		out.AvgHealthScore = scoreSum / float64(scored)
	}
	for _, group := range out.Groups {
		if !group.Managed {
			continue
		}
		switch group.State.Status {
		case domain.GroupAllFused, domain.GroupSurvivorOnly:
			out.GroupsAtRisk++
		}
	}

	out.Tiles = buildTiles(out)
	writeJSON(w, http.StatusOK, out)
}

func buildTiles(o OverviewDTO) []StatTile {
	loadRatio := 0.0
	if o.ConcurrencyLimit > 0 {
		loadRatio = float64(o.AllocatedConc) / float64(o.ConcurrencyLimit) * 100
	}
	return []StatTile{
		{
			Key: "channels", Label: "受管渠道", Value: float64(o.TotalChannels),
			// 各项之和应等于受管总数，否则用户会怀疑数据对不上。
			// 「已关闭」是被人在 sub2api 侧关掉调用的渠道 —— 探测正常但接不到流量。
			Meta: channelBreakdown(o),
			Tone: "primary",
		},
		{
			Key: "score", Label: "平均健康分", Value: round1(o.AvgHealthScore),
			Meta: fmt.Sprintf("%d 个渠道已有样本",
				o.TotalChannels-o.PendingChannels),
			Tone: toneForScore(o.AvgHealthScore),
		},
		{
			Key: "concurrency", Label: "已分配并发", Value: float64(o.AllocatedConc),
			Meta: fmt.Sprintf("上限 %d · 负载 %.0f%%", o.ConcurrencyLimit, loadRatio),
			Tone: "teal",
		},
		{
			Key: "risk", Label: "风险分组", Value: float64(o.GroupsAtRisk),
			Meta: fmt.Sprintf("保底强留 %d 个渠道", o.SurvivorChannels),
			Tone: toneForRisk(o.GroupsAtRisk),
		},
	}
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	v, err := s.buildView(true)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": buildGroupDTOs(v)})
}

func buildGroupDTOs(v view) []GroupDTO {
	byGroup := map[int64][]ChannelDTO{}
	for _, ch := range v.channels {
		for _, ref := range ch.Groups {
			byGroup[ref.ID] = append(byGroup[ref.ID], ch)
		}
	}

	out := make([]GroupDTO, 0, len(v.groups))
	for _, group := range v.groups {
		override := v.overrides[group.ID]
		effective := v.global.ForGroup(override)
		// 空分组必须回 [] 而不是 null。
		//
		// Go 会把 nil 切片序列化成 JSON 的 null，前端 group.channels.length
		// 直接抛 TypeError，整个页面白屏。被排除的分组渠道会被过滤掉，
		// 这里必然是空的，所以这条路径一定会被走到。
		members := byGroup[group.ID]
		if members == nil {
			members = []ChannelDTO{}
		}
		sort.SliceStable(members, func(i, j int) bool {
			if members[i].Weight != members[j].Weight {
				return members[i].Weight > members[j].Weight
			}
			return members[i].HealthScore > members[j].HealthScore
		})

		out = append(out, GroupDTO{
			ID:             group.ID,
			Name:           group.Name,
			Platform:       group.Platform,
			Status:         group.Status,
			RateMultiplier: group.RateMultiplier,
			Managed:        v.global.GroupEnabled(group.ID, override),
			Excluded:       v.global.GroupExcluded(group.ID),
			Strategy:       string(effective.Strategy),
			State:          v.states[group.ID],
			Override:       override,
			Channels:       members,
		})
	}
	return out
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	v, err := s.buildView(true)
	if err != nil {
		writeError(w, err)
		return
	}

	groupFilter := queryInt64Ptr(r, "group_id")
	health := strings.TrimSpace(r.URL.Query().Get("health"))
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	managedOnly := r.URL.Query().Get("managed") == "true"

	items := make([]ChannelDTO, 0, len(v.channels))
	for _, ch := range v.channels {
		if managedOnly && !ch.Managed {
			continue
		}
		if health != "" && health != "all" && ch.Health != health {
			continue
		}
		if groupFilter != nil && !hasGroup(ch, *groupFilter) {
			continue
		}
		if search != "" && !matchesSearch(ch, search) {
			continue
		}
		items = append(items, ch)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Weight != items[j].Weight {
			return items[i].Weight > items[j].Weight
		}
		return items[i].ID < items[j].ID
	})
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) getChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	v, err := s.buildView(false)
	if err != nil {
		writeError(w, err)
		return
	}
	for _, ch := range v.channels {
		if ch.ID != id {
			continue
		}
		samples, err := s.store.RecentSamples(id, 60)
		if err == nil {
			ch.Recent = toSampleDTOs(samples)
		}
		actions, _ := s.store.Actions(id, 30)
		writeJSON(w, http.StatusOK, map[string]any{"channel": ch, "actions": actions})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "渠道不存在"})
}

func (s *Server) probeChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	state, err := s.engine.ProbeAccount(ctx, id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": state})
}

func (s *Server) fuseChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	_ = decodeBody(r, &payload)

	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	if err := s.engine.FuseAccount(ctx, id, payload.Reason); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) recoverChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.engine.RecoverAccount(ctx, id); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// excludeChannel 把渠道加入/移出排除名单。
func (s *Server) excludeChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var payload struct {
		Excluded bool `json:"excluded"`
	}
	if err := decodeBody(r, &payload); err != nil {
		writeError(w, err)
		return
	}

	p, err := s.store.Policy()
	if err != nil {
		writeError(w, err)
		return
	}
	filtered := make([]int64, 0, len(p.ExcludedAccountIDs)+1)
	for _, existing := range p.ExcludedAccountIDs {
		if existing != id {
			filtered = append(filtered, existing)
		}
	}
	if payload.Excluded {
		filtered = append(filtered, id)
	}
	p.ExcludedAccountIDs = filtered

	saved, err := s.store.SavePolicy(p)
	if err != nil {
		writeError(w, err)
		return
	}
	action := "已移出排除名单"
	if payload.Excluded {
		action = "已加入排除名单"
	}
	s.store.Log("info", "exclude_changed", &id, nil, fmt.Sprintf("渠道 #%d %s", id, action), nil)
	s.hub.broadcast()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "policy": saved})
}

// pauseChannel 人工暂停/恢复渠道调度。
//
// 暂停态记在策略的名单里，因此重启和后续每一轮调度都会持续生效，
// 不会像熔断那样被健康分回升自动解除。
func (s *Server) pauseChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var payload struct {
		Paused bool `json:"paused"`
	}
	if err := decodeBody(r, &payload); err != nil {
		writeError(w, err)
		return
	}

	p, err := s.store.Policy()
	if err != nil {
		writeError(w, err)
		return
	}
	filtered := make([]int64, 0, len(p.PausedAccountIDs)+1)
	for _, existing := range p.PausedAccountIDs {
		if existing != id {
			filtered = append(filtered, existing)
		}
	}
	if payload.Paused {
		filtered = append(filtered, id)
	}
	p.PausedAccountIDs = filtered

	if _, err := s.store.SavePolicy(p); err != nil {
		writeError(w, err)
		return
	}

	// 立即写回 sub2api，不等下一轮心跳。
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.engine.SetPaused(ctx, id, payload.Paused); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) updateChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var payload map[string]any
	if err := decodeBody(r, &payload); err != nil {
		writeError(w, err)
		return
	}

	// 调度倍率是 Guardian 自己维护的字段，绝不写回 sub2api。
	if raw, ok := payload["multiplier"]; ok {
		if err := s.saveMultiplier(id, raw); err != nil {
			writeError(w, err)
			return
		}
		delete(payload, "multiplier")
	}

	if len(payload) > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		if err := s.engine.UpdateAccountSettings(ctx, id, payload); err != nil {
			writeError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// saveMultiplier 保存人工设置的调度倍率。
//
// 这是 Guardian 内部字段，只影响价格优先的权重计算，不会写回 sub2api。
// 传 0 或负数表示清除人工设置，回落到按账号类型的默认倍率。
func (s *Server) saveMultiplier(id int64, raw any) error {
	p, err := s.store.Policy()
	if err != nil {
		return err
	}
	key := strconv.FormatInt(id, 10)
	value, ok := raw.(float64)
	if !ok || value <= 0 {
		delete(p.AccountMultipliers, key)
	} else {
		p.AccountMultipliers[key] = value
	}
	if _, err := s.store.SavePolicy(p); err != nil {
		return err
	}
	s.hub.broadcast()
	return nil
}

func (s *Server) channelModels(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	models, err := s.engine.AccountModels(ctx, id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Server) setChannelTestModel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var payload struct {
		ModelID string `json:"model_id"`
	}
	if err := decodeBody(r, &payload); err != nil {
		writeError(w, err)
		return
	}

	p, err := s.store.Policy()
	if err != nil {
		writeError(w, err)
		return
	}
	key := strconv.FormatInt(id, 10)
	if model := strings.TrimSpace(payload.ModelID); model == "" {
		delete(p.AccountTestModels, key)
	} else {
		p.AccountTestModels[key] = model
	}
	saved, err := s.store.SavePolicy(p)
	if err != nil {
		writeError(w, err)
		return
	}
	s.hub.broadcast()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "policy": saved})
}

func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.Policy()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"policy":   p,
		"defaults": policy.Default(),
	})
}

func (s *Server) savePolicy(w http.ResponseWriter, r *http.Request) {
	current, err := s.store.Policy()
	if err != nil {
		writeError(w, err)
		return
	}
	// 以当前策略为底再解码，避免前端漏传字段时把配置清空。
	if err := decodeBody(r, &current); err != nil {
		writeError(w, err)
		return
	}
	saved, err := s.store.SavePolicy(current)
	if err != nil {
		writeError(w, err)
		return
	}
	s.store.Log("info", "policy_updated", nil, nil, "调度策略已更新", nil)
	s.hub.broadcast()
	writeJSON(w, http.StatusOK, map[string]any{"policy": saved})
}

func (s *Server) saveGroupPolicy(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var override policy.GroupOverride
	if err := decodeBody(r, &override); err != nil {
		writeError(w, err)
		return
	}
	if override.Strategy != nil && !override.Strategy.Valid() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "策略取值非法"})
		return
	}
	if err := s.store.SaveGroupOverride(id, override); err != nil {
		writeError(w, err)
		return
	}
	s.store.Log("info", "group_policy_updated", nil, &id,
		fmt.Sprintf("分组 #%d 策略已更新", id), override)
	s.hub.broadcast()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "override": override})
}

// excludeGroup 把整个分组移出/移回调度系统管控。
func (s *Server) excludeGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var payload struct {
		Excluded bool `json:"excluded"`
	}
	if err := decodeBody(r, &payload); err != nil {
		writeError(w, err)
		return
	}

	p, err := s.store.Policy()
	if err != nil {
		writeError(w, err)
		return
	}
	filtered := make([]int64, 0, len(p.ExcludedGroupIDs)+1)
	for _, existing := range p.ExcludedGroupIDs {
		if existing != id {
			filtered = append(filtered, existing)
		}
	}
	if payload.Excluded {
		filtered = append(filtered, id)
	}
	p.ExcludedGroupIDs = filtered

	if _, err := s.store.SavePolicy(p); err != nil {
		writeError(w, err)
		return
	}

	action := "已移回调度系统管控"
	if payload.Excluded {
		action = "已移出调度系统管控"
	}
	s.store.Log("info", "group_exclude_changed", nil, &id,
		fmt.Sprintf("分组 #%d %s", id, action), nil)
	s.hub.broadcast()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) deleteGroupPolicy(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.DeleteGroupOverride(id); err != nil {
		writeError(w, err)
		return
	}
	s.hub.broadcast()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) getConnection(w http.ResponseWriter, r *http.Request) {
	conn, err := s.store.Connection()
	if err != nil {
		writeError(w, err)
		return
	}
	// 不回传明文 Key，只告诉前端是否已配置。
	writeJSON(w, http.StatusOK, map[string]any{
		"base_url":        conn.BaseURL,
		"timeout_seconds": conn.TimeoutSeconds,
		"enabled":         conn.Enabled,
		"has_admin_key":   conn.AdminAPIKey != "",
	})
}

func (s *Server) saveConnection(w http.ResponseWriter, r *http.Request) {
	current, err := s.store.Connection()
	if err != nil {
		writeError(w, err)
		return
	}
	var payload struct {
		BaseURL        *string `json:"base_url"`
		AdminAPIKey    *string `json:"admin_api_key"`
		TimeoutSeconds *int    `json:"timeout_seconds"`
		Enabled        *bool   `json:"enabled"`
	}
	if err := decodeBody(r, &payload); err != nil {
		writeError(w, err)
		return
	}
	if payload.BaseURL != nil {
		current.BaseURL = strings.TrimRight(strings.TrimSpace(*payload.BaseURL), "/")
	}
	// 空字符串表示“不修改”，避免前端不回显 Key 时把它清掉。
	if payload.AdminAPIKey != nil && strings.TrimSpace(*payload.AdminAPIKey) != "" {
		current.AdminAPIKey = strings.TrimSpace(*payload.AdminAPIKey)
	}
	if payload.TimeoutSeconds != nil {
		current.TimeoutSeconds = *payload.TimeoutSeconds
	}
	if payload.Enabled != nil {
		current.Enabled = *payload.Enabled
	}

	if err := s.store.SaveConnection(current); err != nil {
		writeError(w, err)
		return
	}
	s.engine.Reconfigure(current)
	s.store.Log("info", "connection_updated", nil, nil, "sub2api 连接配置已更新", map[string]any{
		"base_url": current.BaseURL,
		"enabled":  current.Enabled,
	})
	s.hub.broadcast()
	s.getConnection(w, r)
}

// sync 立即同步目录并刷新分组聚合。
//
// 用 SyncNow 而不是裸 Sync：同步完还要重算分组状态，
// 否则页面上的分组健康矩阵仍是旧的聚合结果。
func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	if err := s.engine.SyncNow(ctx); err != nil {
		writeError(w, err)
		return
	}
	s.hub.broadcast()

	// 回一份同步结果，让「立即同步」按钮能说清这次到底刷出了什么。
	// 只回 ok 的话，用户点完看不出与网站对上了没有，只能盯着卡片猜。
	summary := map[string]any{"ok": true}
	if accounts, err := s.store.Accounts(); err == nil {
		// 账号按 ID 去重后落在缓存里，这里返回的是本次实际同步到的渠道总数。
		summary["channels"] = len(accounts)
	}
	if v, err := s.buildView(false); err == nil {
		groups, available, healthy, rateLimited, total := 0, 0, 0, 0, 0
		for _, group := range buildGroupDTOs(v) {
			if !group.Managed {
				continue
			}
			groups++
			available += group.State.AvailableAccounts
			healthy += group.State.HealthyAccounts
			rateLimited += group.State.RateLimitedAccounts
			total += group.State.TotalAccounts
		}
		summary["groups"] = groups
		summary["available_accounts"] = available
		// 保留旧字段，避免已有 API 消费方因本次展示口径修正而失效。
		summary["healthy_accounts"] = healthy
		summary["rate_limited_accounts"] = rateLimited
		summary["total_accounts"] = total
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) runOnce(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	if err := s.engine.RunOnce(ctx); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": s.engine.Status()})
}

// cancelRun 中断正在执行的调度轮次与所有探测任务。
// cancelRun 停止自动调度：中断当前轮次并持久化关闭自动守护。
func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	canceled := s.engine.Cancel()
	s.hub.broadcast()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"canceled": canceled,
		"status":   s.engine.Status(),
	})
}

// resumeRun 重新开启自动调度。
func (s *Server) resumeRun(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.Resume(); err != nil {
		writeError(w, err)
		return
	}
	s.hub.broadcast()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"status": s.engine.Status(),
	})
}

func (s *Server) restoreAll(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	restored, err := s.engine.RestoreAll(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	s.store.Log("info", "restore_all", nil, nil,
		fmt.Sprintf("已把 %d 个渠道恢复为接管前的配置", restored), nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restored": restored})
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	filter := store2EventFilter(queryInt(r, "page", 1), queryInt(r, "page_size", 50))
	filter.Level = strings.TrimSpace(r.URL.Query().Get("level"))
	filter.Action = strings.TrimSpace(r.URL.Query().Get("action"))
	filter.AccountID = queryInt64Ptr(r, "account_id")
	filter.GroupID = queryInt64Ptr(r, "group_id")

	items, total, err := s.store.Events(filter)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, EventPage{
		Items:    items,
		Total:    total,
		Page:     filter.Page,
		PageSize: filter.PageSize,
	})
}

func (s *Server) listActions(w http.ResponseWriter, r *http.Request) {
	accountID := int64(0)
	if ref := queryInt64Ptr(r, "account_id"); ref != nil {
		accountID = *ref
	}
	actions, err := s.store.Actions(accountID, queryInt(r, "limit", 100))
	if err != nil {
		writeError(w, err)
		return
	}

	// 回填渠道名与分组，避免页面上只有一串裸 ID。
	accounts, err := s.store.Accounts()
	if err != nil {
		writeError(w, err)
		return
	}
	byID := make(map[int64]domain.Account, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	groups, err := s.store.Groups()
	if err != nil {
		writeError(w, err)
		return
	}
	groupNames := make(map[int64]string, len(groups))
	for _, group := range groups {
		groupNames[group.ID] = group.Name
	}

	items := make([]ActionDTO, 0, len(actions))
	for _, action := range actions {
		dto := ActionDTO{Action: action}
		account, ok := byID[action.AccountID]
		if !ok {
			// 账号已被删除（可能正是这条记录干的），只能显示 ID。
			dto.Deleted = true
			dto.AccountName = fmt.Sprintf("已移除的渠道 #%d", action.AccountID)
			items = append(items, dto)
			continue
		}
		dto.AccountName = account.Name
		dto.Platform = account.Platform
		for _, groupID := range account.GroupIDSet() {
			if name, ok := groupNames[groupID]; ok {
				dto.Groups = append(dto.Groups, GroupRef{ID: groupID, Name: name})
			}
		}
		items = append(items, dto)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func toSampleDTOs(samples []domain.Sample) []SampleDTO {
	out := make([]SampleDTO, 0, len(samples))
	for _, sample := range samples {
		out = append(out, SampleDTO{
			OccurredAt: sample.OccurredAt,
			Source:     string(sample.Source),
			EventType:  string(sample.EventType),
			Score:      sample.Score,
			TTFBMs:     sample.TTFBMs,
			StatusCode: sample.StatusCode,
			Message:    sample.Message,
		})
	}
	return out
}

func hasGroup(ch ChannelDTO, groupID int64) bool {
	for _, ref := range ch.Groups {
		if ref.ID == groupID {
			return true
		}
	}
	return false
}

func matchesSearch(ch ChannelDTO, needle string) bool {
	fields := []string{ch.Name, ch.Platform, ch.Type, ch.TestModel, strconv.FormatInt(ch.ID, 10)}
	for _, ref := range ch.Groups {
		fields = append(fields, ref.Name)
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}

// effectiveHealth 返回渠道对外展示的健康态。
//
// 排除与暂停是**即时生效的策略事实**，不该等下一轮调度才反映到界面上：
// 用户点了排除却看到状态仍是「健康」，会以为没生效；
// 「已排除」页签也会因此计数为 0，让人找不到自己刚排除的渠道。
//
// 其余状态（健康 / 降级 / 熔断等）是引擎的计算结果，只能来自 channel_states。
func effectiveHealth(
	global policy.Policy,
	account domain.Account,
	state domain.ChannelState,
) domain.ChannelHealth {
	if global.AccountExcluded(account.ID) {
		return domain.HealthExcluded
	}
	if global.AccountPaused(account.ID) {
		return domain.HealthPaused
	}

	// 反向也要成立：已移出名单但状态还没刷新时，不能继续显示排除/暂停。
	switch state.Health {
	case "", domain.HealthExcluded, domain.HealthPaused:
		return domain.HealthUnknown
	}
	return state.Health
}

// applyPending 判断渠道是否有尚未在 sub2api 生效的期望状态。
//
// 排除与暂停是即时生效的策略事实（见 effectiveHealth），此时不该再报待生效
// —— 否则刚点完排除就会同时看到「已排除」和「熔断待生效」两个互相矛盾的提示。
func applyPending(
	global policy.Policy,
	account domain.Account,
	state domain.ChannelState,
) bool {
	if global.AccountExcluded(account.ID) || global.AccountPaused(account.ID) {
		return false
	}
	return state.ApplyPending
}

func firstNonEmpty(items ...string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return ""
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}

func toneForScore(score float64) string {
	switch {
	case score >= 85:
		return "success"
	case score >= 60:
		return "warning"
	default:
		return "danger"
	}
}

func toneForRisk(count int) string {
	if count > 0 {
		return "danger"
	}
	return "success"
}

// channelBreakdown 拼出受管渠道的构成说明。
//
// 各项之和等于受管总数：健康 + 降级 + 熔断 + 保底 + 待探测 + 已关闭。
// 数字加起来对不上的话，用户第一反应是「这面板的数据不可信」——
// 所以「已关闭」（探测正常但被人在 sub2api 侧关掉调用）也要显式列出来，
// 而不是让它从统计里凭空消失。
func channelBreakdown(o OverviewDTO) string {
	parts := []string{
		fmt.Sprintf("健康 %d", o.HealthyChannels),
		fmt.Sprintf("降级 %d", o.DegradedChannels),
		fmt.Sprintf("熔断 %d", o.FusedChannels),
	}
	// 限流是降级的子集，括注在降级之后而不是另起一项 —— 否则各项之和会超过总数。
	if o.RateLimitedChannels > 0 {
		parts[1] = fmt.Sprintf("降级 %d（含限流 %d）", o.DegradedChannels, o.RateLimitedChannels)
	}
	if o.SurvivorChannels > 0 {
		parts = append(parts, fmt.Sprintf("保底 %d", o.SurvivorChannels))
	}
	if o.PendingChannels > 0 {
		parts = append(parts, fmt.Sprintf("待探测 %d", o.PendingChannels))
	}
	if o.UnschedulableChannels > 0 {
		parts = append(parts, fmt.Sprintf("已关闭 %d", o.UnschedulableChannels))
	}
	return strings.Join(parts, " · ")
}
