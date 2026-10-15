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

var ErrRunning = errors.New("已有降智检测正在运行")

var errSchedulePaused = errors.New("进入免检时段或检测配置已停用，本轮停止")

type Manager struct {
	store   *store.Store
	client  *upstream.Client
	http    *http.Client
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	running bool
	started bool
	closed  bool
	wg      sync.WaitGroup
}

func New(st *store.Store, client *upstream.Client) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{store: st, client: client, ctx: ctx, cancel: cancel, http: &http.Client{
		Timeout:       3 * time.Minute,
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

func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
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
	if err := m.begin(); err != nil {
		return nil
	}
	defer m.end()
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
		runs, err := m.store.ModelCheckRuns(target.AccountID, target.Model, 0, 1)
		if err != nil {
			return err
		}
		if len(runs) > 0 && time.Now().Before(runs[0].StartedAt.Add(time.Duration(current.IntervalMinutes)*time.Minute)) {
			continue
		}
		// A single worker bounds upstream load and prevents manual/scheduled overlap.
		err = m.run(target, true)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) begin() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("检测服务已停止")
	}
	if m.running {
		return ErrRunning
	}
	m.running = true
	m.wg.Add(1)
	return nil
}

func (m *Manager) end() {
	m.mu.Lock()
	m.running = false
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
	if err := m.begin(); err != nil {
		return err
	}
	go func() {
		defer m.end()
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
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Minute)
	defer cancel()
	var credentials upstream.AccountCredentials
	if err == nil {
		credentials, err = m.client.ExportAccountCredentials(ctx, target.AccountID)
		if err != nil {
			err = errors.New("无法读取渠道连接信息，请检查 sub2api 管理权限与渠道凭据")
		}
	}
	protocol := "chat/completions"
	if account.Platform == "anthropic" || strings.HasPrefix(target.Model, "claude-") || strings.HasPrefix(target.ExpectedModel, "claude-") {
		protocol = "messages"
	}
	lengths := rand.Perm(41)[:3]
	var outputs [][]int
	for i := 0; i < 3 && err == nil; i++ {
		// A round already in flight may finish; do not start another challenge in quiet hours.
		if scheduled {
			current, readErr := m.store.ModelCheckSettings()
			if readErr != nil {
				err = errors.New("无法读取检测配置")
				break
			}
			if !current.Enabled || quietAt(current, time.Now()) || !slices.Contains(current.Targets, target) {
				err = errSchedulePaused
				break
			}
		}
		count := 292 + lengths[i]
		prompt := challenge(count)
		var output string
		var status int
		output, status, err = requestOutput(ctx, m.http, credentials, target.Model, prompt, protocol)
		if i == 0 && (status == 404 || status == 405) {
			if protocol == "messages" {
				protocol = "chat/completions"
			} else {
				protocol = "responses"
			}
			output, _, err = requestOutput(ctx, m.http, credentials, target.Model, prompt, protocol)
		}
		if err != nil {
			break
		}
		numbers := parseNumbers(output)
		run.NumberCounts = append(run.NumberCounts, len(numbers))
		if len(numbers) < (count*55+99)/100 {
			continue
		}
		outputs = append(outputs, numbers)
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
		run.Status, run.Message = classify(target.ExpectedModel, run.Prediction, run.Probability)
	}
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	return m.store.FinishModelCheckRun(run)
}

func classify(expected, prediction string, probability float64) (string, string) {
	if expected == "" {
		return "unverified", "未指定库内预期模型，仅展示候选归因，不判断降智"
	}
	if probability < 0.8 {
		return "inconclusive", "最高候选概率低于 80%，证据不足"
	}
	if expected == prediction {
		return "consistent", "指纹与预期模型一致（仅供参考）"
	}
	return "suspected", "指纹与预期模型不一致，疑似替换或降智，建议复测"
}
