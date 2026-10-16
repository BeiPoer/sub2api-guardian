package modelcheck

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"sub2api-guardian/backend/internal/policy"
	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
)

var ErrRunning = errors.New("此渠道模型已有降智检测正在运行")

var errSchedulePaused = errors.New("进入免检时段或检测配置已停用，本轮停止")

type Manager struct {
	store     *store.Store
	client    *upstream.Client
	http      *http.Client
	waitRetry func(context.Context, time.Duration) error
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	running   map[targetKey]store.ModelCheckTarget
	started   bool
	closed    bool
	wg        sync.WaitGroup
}

type targetKey struct {
	accountID int64
	model     string
}

func New(st *store.Store, client *upstream.Client) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{store: st, client: client, ctx: ctx, cancel: cancel, waitRetry: waitForRetry, running: make(map[targetKey]store.ModelCheckTarget), http: &http.Client{
		Timeout:       10 * time.Minute,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (m *Manager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started || m.closed {
		return
	}
	if err := m.store.InterruptModelCheckRuns(); err != nil {
		log.Printf("恢复降智检测记录失败: %v", err)
		return
	}
	m.started = true
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			if err := m.runDue(); err != nil {
				log.Printf("降智检测调度失败: %v", err)
			}
			select {
			case <-m.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (m *Manager) Stop() {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) RunningTargets() []store.ModelCheckTarget {
	m.mu.Lock()
	defer m.mu.Unlock()
	targets := make([]store.ModelCheckTarget, 0, len(m.running))
	for _, target := range m.running {
		targets = append(targets, target)
	}
	return targets
}

func (m *Manager) Save(settings store.ModelCheckSettings) error {
	if settings.Targets == nil {
		settings.Targets = []store.ModelCheckTarget{}
	}
	if err := validateSettings(settings); err != nil {
		return err
	}
	for _, target := range settings.Targets {
		if err := m.validateTarget(target); err != nil {
			return err
		}
	}
	return m.store.SaveModelCheckSettings(settings)
}

func validateSettings(v store.ModelCheckSettings) error {
	if v.IntervalMinutes < 1 || v.IntervalMinutes > 10080 {
		return errors.New("检测间隔须为 1–10080 分钟")
	}
	if len(v.Targets) > 500 {
		return errors.New("最多配置 500 个渠道模型组合")
	}
	if v.Enabled && len(v.Targets) == 0 {
		return errors.New("请至少选择一个渠道模型")
	}
	if _, err := time.LoadLocation(v.Timezone); err != nil || v.Timezone == "" {
		return errors.New("时区无效")
	}
	for _, clock := range []string{v.QuietStart, v.QuietEnd} {
		parsed, err := time.Parse("15:04", clock)
		if err != nil || parsed.Format("15:04") != clock {
			return errors.New("免检时间须为 HH:mm 格式")
		}
	}
	if v.QuietEnabled && v.QuietStart == v.QuietEnd {
		return errors.New("免检开始和结束时间不能相同；全天检测请关闭免检时段")
	}
	seen := map[string]bool{}
	for _, target := range v.Targets {
		if target.AccountID <= 0 || target.GroupID < 0 || strings.TrimSpace(target.Model) == "" || len(target.Model) > 200 || strings.TrimSpace(target.Model) != target.Model || strings.ContainsAny(target.Model, "\r\n\x00") {
			return errors.New("渠道或模型无效")
		}
		if target.ExpectedModel != "" && !slices.Contains(Models(), target.ExpectedModel) {
			return errors.New("预期模型不在指纹库中")
		}
		key := fmt.Sprintf("%d/%s", target.AccountID, target.Model)
		if seen[key] {
			return errors.New("同一渠道模型不能重复选择")
		}
		seen[key] = true
	}
	return nil
}

func (m *Manager) validateTarget(target store.ModelCheckTarget) error {
	account, err := m.store.Account(target.AccountID)
	if err != nil {
		return fmt.Errorf("渠道 #%d 不存在，请先同步渠道池", target.AccountID)
	}
	if !policy.IsAPIKeyType(account.Type) {
		return fmt.Errorf("渠道 %s 不支持：目前仅支持 API Key 类型", account.Name)
	}
	if target.GroupID != 0 && !slices.Contains(account.GroupIDSet(), target.GroupID) {
		return fmt.Errorf("渠道 %s 已不在所选渠道池分组中", account.Name)
	}
	return nil
}

func quietAt(v store.ModelCheckSettings, now time.Time) bool {
	if !v.QuietEnabled {
		return false
	}
	location, err := time.LoadLocation(v.Timezone)
	if err != nil {
		return true
	}
	clock := now.In(location).Format("15:04")
	if v.QuietStart < v.QuietEnd {
		return clock >= v.QuietStart && clock < v.QuietEnd
	}
	return clock >= v.QuietStart || clock < v.QuietEnd
}

func (m *Manager) runDue() error {
	settings, err := m.store.ModelCheckSettings()
	if err != nil {
		return err
	}
	if !settings.Enabled || quietAt(settings, time.Now()) {
		return nil
	}
	for _, target := range settings.Targets {
		if m.ctx.Err() != nil {
			return nil
		}
		current, err := m.store.ModelCheckSettings()
		if err != nil {
			return err
		}
		if !current.Enabled || quietAt(current, time.Now()) {
			return nil
		}
		if !slices.Contains(current.Targets, target) {
			continue
		}
		if err := m.runDueTarget(target, current.IntervalMinutes); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) runDueTarget(target store.ModelCheckTarget, intervalMinutes int) error {
	if err := m.begin(target); err != nil {
		if errors.Is(err, ErrRunning) {
			return nil
		}
		return err
	}
	defer m.end(target)
	// Reserve the target before reading its last run so a manual run cannot slip in between.
	runs, err := m.store.ModelCheckRuns(target.AccountID, target.Model, 0, 1)
	if err != nil {
		return err
	}
	if len(runs) > 0 && time.Now().Before(runs[0].StartedAt.Add(time.Duration(intervalMinutes)*time.Minute)) {
		return nil
	}
	return m.run(target, true)
}

func (m *Manager) begin(target store.ModelCheckTarget) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("检测服务已停止")
	}
	key := targetKey{target.AccountID, target.Model}
	if _, exists := m.running[key]; exists {
		return ErrRunning
	}
	m.running[key] = target
	m.wg.Add(1)
	return nil
}

func (m *Manager) end(target store.ModelCheckTarget) {
	m.mu.Lock()
	delete(m.running, targetKey{target.AccountID, target.Model})
	m.mu.Unlock()
	m.wg.Done()
}

func (m *Manager) RunNow(target store.ModelCheckTarget) error {
	settings, err := m.store.ModelCheckSettings()
	if err != nil {
		return err
	}
	if !slices.Contains(settings.Targets, target) {
		return errors.New("请先保存此渠道模型的检测配置")
	}
	if err := m.validateTarget(target); err != nil {
		return err
	}
	if err := m.begin(target); err != nil {
		return err
	}
	go func() {
		defer m.end(target)
		if err := m.run(target, false); err != nil {
			log.Printf("保存降智检测结果失败: %v", err)
		}
	}()
	return nil
}

func (m *Manager) run(target store.ModelCheckTarget, scheduled bool) error {
	account, err := m.store.Account(target.AccountID)
	run := store.ModelCheckRun{ModelCheckTarget: target, AccountName: account.Name, StartedAt: time.Now().UTC(), Status: "running", BankVersion: bank.BuiltAt, NumberCounts: []int{}, Candidates: []store.ModelCheckCandidate{}}
	run, saveErr := m.store.CreateModelCheckRun(run)
	if saveErr != nil {
		return saveErr
	}
	if err == nil {
		err = m.validateTarget(target)
	}
	// Allow three challenges, their retries, and protocol fallback to use the per-request timeout.
	ctx, cancel := context.WithTimeout(m.ctx, 150*time.Minute)
	defer cancel()
	var credentials upstream.AccountCredentials
	if err == nil {
		err = m.retryRequest(ctx, &run, scheduled, "读取渠道连接信息", func() error {
			var exportErr error
			credentials, exportErr = m.client.ExportAccountCredentials(ctx, target.AccountID)
			if exportErr != nil {
				return errors.New("无法读取渠道连接信息，请检查 sub2api 管理权限与渠道凭据")
			}
			return nil
		})
	}
	protocol := "chat/completions"
	if account.Platform == "anthropic" || strings.HasPrefix(target.Model, "claude-") || strings.HasPrefix(target.ExpectedModel, "claude-") {
		protocol = "messages"
	}
	lengths := rand.Perm(41)[:3]
	var outputs [][]int
	for i := 0; i < 3 && err == nil; i++ {
		count := 292 + lengths[i]
		prompt := challenge(count)
		var output string
		var status int
		fallbackTried := false
		err = m.retryRequest(ctx, &run, scheduled, fmt.Sprintf("第 %d/3 条挑战", i+1), func() error {
			var requestErr error
			output, status, requestErr = requestOutput(ctx, m.http, credentials, target.Model, prompt, protocol)
			if i == 0 && !fallbackTried && (status == 404 || status == 405) {
				fallbackTried = true
				if protocol == "messages" {
					protocol = "chat/completions"
				} else {
					protocol = "responses"
				}
				if checkErr := m.checkRunAllowed(ctx, target, scheduled); checkErr != nil {
					return checkErr
				}
				output, _, requestErr = requestOutput(ctx, m.http, credentials, target.Model, prompt, protocol)
			}
			return requestErr
		})
		if err != nil {
			break
		}
		numbers := parseNumbers(output)
		run.NumberCounts = append(run.NumberCounts, len(numbers))
		if len(numbers) < (count*55+99)/100 {
			continue
		}
		outputs = append(outputs, numbers)
		run.ValidOutputs = len(outputs)
	}
	run.ValidOutputs = len(outputs)
	switch {
	case err != nil:
		run.Status = "error"
		run.Message = err.Error()
		if errors.Is(err, errSchedulePaused) {
			run.Status = "interrupted"
		}
		if m.ctx.Err() != nil {
			run.Status = "interrupted"
			run.Message = "服务停止，检测已取消"
		}
	case len(outputs) < 3:
		run.Status = "inconclusive"
		run.Message = "未获得三份有效数字序列，可能拒答或输出截断，无法判断"
	default:
		run.Candidates = analyze(outputs)
		run.Prediction = run.Candidates[0].Model
		run.Probability = run.Candidates[0].Probability
		run.Status, run.Message = classify(target.ExpectedModel, run.Candidates)
	}
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	return m.store.FinishModelCheckRun(run)
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (m *Manager) checkRunAllowed(ctx context.Context, target store.ModelCheckTarget, scheduled bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !scheduled {
		return nil
	}
	current, err := m.store.ModelCheckSettings()
	if err != nil {
		return errors.New("无法读取检测配置")
	}
	if !current.Enabled || quietAt(current, time.Now()) || !slices.Contains(current.Targets, target) {
		return errSchedulePaused
	}
	return nil
}

func (m *Manager) retryRequest(ctx context.Context, run *store.ModelCheckRun, scheduled bool, step string, request func() error) error {
	delays := [...]time.Duration{3 * time.Second, 10 * time.Second, 30 * time.Second}
	for attempt := 0; ; attempt++ {
		if err := m.checkRunAllowed(ctx, run.ModelCheckTarget, scheduled); err != nil {
			return err
		}
		run.Message = "检测中：" + step
		if attempt > 0 {
			run.Message += fmt.Sprintf("，正在进行第 %d/3 次重试", attempt)
		}
		if err := m.store.FinishModelCheckRun(*run); err != nil {
			return err
		}
		err := request()
		if err == nil {
			return nil
		}
		if errors.Is(err, errInvalidOutput) || errors.Is(err, errSchedulePaused) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt == len(delays) {
			return fmt.Errorf("%s失败（已重试 3 次）：%w", step, err)
		}
		if checkErr := m.checkRunAllowed(ctx, run.ModelCheckTarget, scheduled); checkErr != nil {
			return checkErr
		}
		run.Message = fmt.Sprintf("检测中：%s失败（%s），%d 秒后进行第 %d/3 次重试", step, err.Error(), int(delays[attempt]/time.Second), attempt+1)
		if saveErr := m.store.FinishModelCheckRun(*run); saveErr != nil {
			return saveErr
		}
		if waitErr := m.waitRetry(ctx, delays[attempt]); waitErr != nil {
			return waitErr
		}
	}
}

// Reclassify applies the current verdict to stored successful attribution without another probe.
func Reclassify(run *store.ModelCheckRun) {
	switch run.Status {
	case "consistent", "suspected", "inconclusive", "unverified":
		if run.ValidOutputs >= 3 && len(run.Candidates) > 0 {
			run.Status, run.Message = classify(run.ExpectedModel, run.Candidates)
		}
	}
}

func classify(expected string, candidates []store.ModelCheckCandidate) (string, string) {
	if expected == "" {
		return "unverified", "未指定库内预期模型，仅展示候选归因，不判断降智"
	}
	for _, candidate := range candidates {
		if candidate.Model != expected {
			continue
		}
		switch {
		case candidate.Probability >= 0.8:
			return "consistent", "预期模型的候选库内概率达到 80%，指纹与预期一致（仅供参考）"
		case candidate.Probability <= 0.2:
			return "suspected", "预期模型的候选库内概率不高于 20%，疑似与预期不符；具体替代模型仍可能不确定，建议复测"
		default:
			return "inconclusive", "预期模型的候选库内概率介于 20% 与 80% 之间，尚不足以判断是否符合预期"
		}
	}
	return "unverified", "本次候选结果未包含预期模型，仅展示归因，不判断是否符合预期"
}
