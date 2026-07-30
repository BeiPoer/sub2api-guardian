package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
	"sub2api-guardian/backend/internal/scoring"
	"sub2api-guardian/backend/internal/upstream"
)

// collect 为本轮需要采样的渠道拉取真实流量并按需补探针。
//
// 返回本轮实际探测的渠道数和新增样本数。
func (e *Engine) collect(ctx context.Context, r *round) (probed, samples int) {
	type task struct {
		ch          *channel
		wantTraffic bool
		wantProbe   bool
	}

	tasks := make([]task, 0, len(r.channels))
	for _, ch := range r.channels {
		if ch.excluded {
			continue
		}
		t := task{ch: ch}
		t.wantTraffic = r.monitoringOK && ch.pol.Traffic.Enabled &&
			olderThan(ch.state.LastTrafficAt, ch.pol.Traffic.RefreshSeconds, r.now)
		t.wantProbe = shouldProbe(ch, r.now)
		if t.wantTraffic || t.wantProbe {
			tasks = append(tasks, t)
		}
	}
	if len(tasks) == 0 {
		return 0, 0
	}

	workers := r.global.Probe.Concurrency
	if workers <= 0 {
		workers = 4
	}
	if workers > len(tasks) {
		workers = len(tasks)
	}

	var (
		mu        sync.Mutex
		jobs      = make(chan task)
		wg        sync.WaitGroup
		probeCnt  int
		sampleCnt int
	)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				var added int
				if job.wantTraffic {
					added += e.pullTraffic(ctx, job.ch)
				}
				if job.wantProbe {
					if e.probeOne(ctx, job.ch) {
						mu.Lock()
						probeCnt++
						mu.Unlock()
						added++
					}
				}
				mu.Lock()
				sampleCnt += added
				mu.Unlock()
			}
		}()
	}

	for _, job := range tasks {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return probeCnt, sampleCnt
		case jobs <- job:
		}
	}
	close(jobs)
	wg.Wait()
	return probeCnt, sampleCnt
}

// shouldProbe 判断某渠道本轮是否需要主动探测。
//
// 规则：
//  1. 已熔断的渠道走恢复探测，只受 Recovery.Enabled 约束（低频间隔）；
//  2. 其余渠道受探测总开关约束；
//  3. 有新鲜的真实流量样本时可跳过探测（可配置）；
//  4. 其余按探测间隔执行；从未采样过的渠道立即探测一次。
//
// 熔断分支刻意排在总开关之前：熔断的渠道 schedulable=false，拿不到任何真实
// 流量，主动探测是它唯一的健康信号来源，也就是熔断唯一的退出条件。
// 早期实现把总开关放在前面，结果关掉常规探测后熔断渠道永远回不了池。
//
// 两个开关的语义因此是分开的：Probe.Enabled 管常规巡检的压力，
// Recovery.Enabled 管熔断渠道要不要自动复活。
func shouldProbe(ch *channel, now time.Time) bool {
	p := ch.pol
	if ch.state.Health == domain.HealthFused {
		if !p.Recovery.Enabled {
			return false
		}
		return olderThan(ch.state.LastProbeAt, p.Recovery.ProbeIntervalSeconds, now)
	}
	if !p.Probe.Enabled {
		return false
	}
	if ch.state.LastSampleAt.IsZero() {
		return true
	}
	if p.Probe.SkipWhenTrafficFresh && !olderThan(ch.state.LastSampleAt, p.Probe.TrafficFreshSeconds, now) {
		return false
	}
	return olderThan(ch.state.LastProbeAt, p.Probe.IntervalSeconds, now)
}

// pullTraffic 拉取真实请求记录并转换为样本，返回新增条数。
func (e *Engine) pullTraffic(ctx context.Context, ch *channel) int {
	p := ch.pol
	lookback := time.Duration(p.Traffic.LookbackMinutes) * time.Minute
	details, err := e.client.ListAccountRequests(ctx, ch.account.ID, lookback, p.Traffic.MaxSamplesPerAccount)
	if err != nil {
		if errors.Is(err, upstream.ErrMonitoringDisabled) {
			return 0
		}
		e.store.Log("warn", "traffic_pull_failed", accountRef(ch.account.ID), nil, err.Error(), nil)
		return 0
	}

	ch.state.LastTrafficAt = time.Now()

	added := 0
	for _, detail := range details {
		sample := trafficSample(ch.account.ID, detail, p)
		if sample.RequestID == "" {
			continue
		}
		inserted, err := e.store.AddSampleIfNew(sample)
		if err != nil {
			e.store.Log("warn", "sample_write_failed", accountRef(ch.account.ID), nil, err.Error(), nil)
			continue
		}
		if inserted {
			added++
		}
	}
	return added
}

// trafficSample 把一条真实请求记录转换为健康样本。
//
// 注意：ops 记录只有整体 duration_ms，没有首字时间，因此流式请求的首字延迟仍以探针为准。
func trafficSample(accountID int64, detail upstream.RequestDetail, p policy.Policy) domain.Sample {
	var (
		duration   int64
		statusCode int
	)
	if detail.DurationMs != nil {
		duration = int64(*detail.DurationMs)
	}
	if detail.StatusCode != nil {
		statusCode = *detail.StatusCode
	}

	success := strings.EqualFold(detail.Kind, "success")
	in := scoring.ClassifyInput{
		Success:    success,
		StatusCode: statusCode,
		Message:    detail.Message,
		TTFBMs:     duration,
	}
	event := scoring.Classify(in, p)

	occurred := detail.CreatedAt
	if occurred.IsZero() {
		occurred = time.Now()
	}
	return domain.Sample{
		AccountID:  accountID,
		OccurredAt: occurred,
		Source:     domain.SourceTraffic,
		EventType:  event,
		Score:      scoring.ScoreFor(event, p),
		TTFBMs:     duration,
		DurationMs: duration,
		StatusCode: statusCode,
		Model:      detail.Model,
		RequestID:  detail.RequestID,
		Message:    detail.Message,
	}
}

// probeOne 对单个渠道做一次探测并写入样本，返回是否真的探测了。
func (e *Engine) probeOne(ctx context.Context, ch *channel) bool {
	p := ch.pol
	timeout := time.Duration(p.Probe.TimeoutSeconds) * time.Second
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	model := probeModel(ch, p)
	result, err := e.client.Probe(probeCtx, ch.account.ID, model, p.Probe.Prompt)

	message := result.Message
	if message == "" && err != nil {
		message = err.Error()
	}
	in := scoring.ClassifyInput{
		Success:    result.Success,
		StatusCode: result.StatusCode,
		Message:    message,
		TTFBMs:     result.TTFBMs,
		Timeout:    result.Timeout,
	}
	event := scoring.Classify(in, p)

	// 样本记录 sub2api 实际使用的模型，没有回传时退回请求的模型。
	sample := domain.Sample{
		AccountID:    ch.account.ID,
		OccurredAt:   time.Now(),
		Source:       domain.SourceProbe,
		EventType:    event,
		Score:        scoring.ScoreFor(event, p),
		TTFBMs:       result.TTFBMs,
		DurationMs:   result.DurationMs,
		StatusCode:   result.StatusCode,
		Model:        firstNonEmpty(result.ActualModel, model),
		RequestModel: model,
		Message:      message,
	}
	if err := e.store.AddSample(sample); err != nil {
		e.store.Log("warn", "sample_write_failed", accountRef(ch.account.ID), nil, err.Error(), nil)
		return false
	}

	ch.state.LastProbeAt = sample.OccurredAt
	ch.state.LastProbeModel = sample.Model
	ch.state.LastRequestModel = model

	// sub2api 把请求的模型改掉了：这是「我指定了模型却没生效」的唯一线索，
	// 必须暴露出来。根源是账号级的通配符模型映射，只能在 sub2api 侧调整。
	if result.ModelRewritten() {
		ch.state.ModelRewritten = true
		e.store.Log("warn", "probe_model_rewritten", accountRef(ch.account.ID), accountRef(ch.primaryGroup),
			fmt.Sprintf("渠道 %s 指定测活模型 %q，但 sub2api 实际使用了 %q（账号级模型映射所致，需在 sub2api 侧调整该账号的模型映射）",
				ch.account.Name, model, result.ActualModel),
			map[string]any{
				"requested_model": model,
				"actual_model":    result.ActualModel,
			})
	} else {
		ch.state.ModelRewritten = false
	}

	if scoring.IsFailure(event) {
		ch.state.LastError = message
		e.store.Log("warn", "probe_failed", accountRef(ch.account.ID), accountRef(ch.primaryGroup),
			shorten(message, 200), map[string]any{
				"event":           string(event),
				"requested_model": model,
				"actual_model":    sample.Model,
				"ttfb":            sample.TTFBMs,
				"status":          sample.StatusCode,
			})
	} else {
		ch.state.LastError = ""
	}
	return true
}

// probeModel 决定探测使用的模型：账号专属 → 全局默认 → 已知模型列表首项。
func probeModel(ch *channel, p policy.Policy) string {
	if model := strings.TrimSpace(p.AccountTestModels[itoa(ch.account.ID)]); model != "" {
		return model
	}
	if model := strings.TrimSpace(p.Probe.Model); model != "" {
		return model
	}
	if len(ch.state.Models) > 0 {
		return ch.state.Models[0]
	}
	return ""
}

// loadSamplesAndScore 读取样本并计算每个渠道的健康分。
func (e *Engine) loadSamplesAndScore(r *round) {
	for _, ch := range r.channels {
		window := ch.pol.Scoring.LongWindow
		samples, err := e.store.RecentSamples(ch.account.ID, window)
		if err != nil {
			e.store.Log("warn", "sample_read_failed", accountRef(ch.account.ID), nil, err.Error(), nil)
			continue
		}
		ch.samples = samples
		ch.score = scoring.Compute(samples, ch.pol)
		if len(samples) > 0 {
			ch.state.LastSampleAt = samples[0].OccurredAt
		}
		ch.state.ShortScore = ch.score.Short
		ch.state.LongScore = ch.score.Long
		ch.state.HealthScore = ch.score.Final
		ch.state.SampleCount = ch.score.SampleCount
		ch.state.ConsecutiveOK = ch.score.ConsecutiveOK
		ch.state.ConsecutiveFail = ch.score.ConsecutiveFail
		ch.state.TTFBP50Ms = ch.score.TTFBP50Ms
		ch.state.TTFBP95Ms = ch.score.TTFBP95Ms
		ch.state.Balance = ch.account.Balance()
	}
}

func olderThan(last time.Time, seconds int, now time.Time) bool {
	if last.IsZero() {
		return true
	}
	if seconds <= 0 {
		return true
	}
	return now.Sub(last) >= time.Duration(seconds)*time.Second
}

func firstNonEmpty(items ...string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return ""
}

func shorten(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
