package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub2api-guardian/backend/internal/domain"
	"sub2api-guardian/backend/internal/policy"
)

// TestPolicyMigrationFillsNewFields 覆盖「老策略缺新字段」的升级路径。
//
// 策略以 JSON 整体存储，新增字段在老记录里会反序列化成零值。对 bool 和
// 「0 是合法值」的数值，Normalize 的「<=0 视为未设置」兜底不成立：
//   - http_degrade_only 新默认 true，老库读成 false → 用户仍在用旧的熔断行为
//   - quota_exhausted 新默认 15，老库读成 0 → 0 分低于回池目标分，渠道永久出局
func TestPolicyMigrationFillsNewFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guardian.sqlite")

	// 先建库，然后写入一份「缺新字段」的老策略。
	st, err := Open(path)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}

	legacy := map[string]any{
		"strategy": "speed",
		"scoring": map[string]any{
			// 刻意不含 quota_exhausted。
			"event_scores": map[string]any{
				"perfect": 100, "slow_ttfb": 65, "upstream_unknown": 40,
				"gateway_error": 25, "probe_fail": 10, "fatal": 0,
			},
			"short_window": 10, "long_window": 60,
			"latest_weight": 0.5, "short_ratio": 0.7, "slow_ttfb_ms": 5000,
		},
		// 刻意不含 http_degrade_only / latency_degrade_only。
		"breaker": map[string]any{
			"enabled": true, "hard_fatal": false,
			"http_window": 5, "http_failures": 3, "http_score_below": 60,
			"latency_window": 10, "latency_occurrences": 5, "latency_ttfb_ms": 15000,
			"max_switch_per_round": 1, "min_pool_size": 1, "min_pool_score": 3,
			"fused_cooldown_seconds": 180, "instant_status_codes": []int{401, 403},
		},
	}
	raw, _ := json.Marshal(legacy)
	if err := st.setMeta(metaPolicy, string(raw)); err != nil {
		t.Fatalf("写入老策略失败: %v", err)
	}
	_ = st.Close()

	// 重新打开：迁移应在此时补齐字段。
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	defer func() { _ = st2.Close() }()

	p, err := st2.Policy()
	if err != nil {
		t.Fatalf("读取策略失败: %v", err)
	}
	d := policy.Default()

	if p.Scoring.EventScores.QuotaExhausted != d.Scoring.EventScores.QuotaExhausted {
		t.Fatalf("限流分值 = %.1f, 期望补成默认值 %.1f（0 分会让渠道永久出局）",
			p.Scoring.EventScores.QuotaExhausted, d.Scoring.EventScores.QuotaExhausted)
	}
	if !p.Breaker.HTTPDegradeOnly {
		t.Fatal("http_degrade_only 应补成新默认值 true，否则老用户仍在用旧的熔断行为")
	}
	if !p.Breaker.LatencyDegradeOnly {
		t.Fatal("latency_degrade_only 应补成新默认值 true")
	}

	// 用户原有的设置必须保留。
	if p.Strategy != policy.StrategySpeed {
		t.Fatalf("策略 = %v, 用户原有设置应保留", p.Strategy)
	}
	if p.Breaker.HardFatal {
		t.Fatal("用户关掉的 hard_fatal 不该被迁移打开")
	}
	if len(p.Breaker.InstantStatusCodes) != 2 {
		t.Fatalf("用户配的立即熔断状态码应保留，实际 %v", p.Breaker.InstantStatusCodes)
	}
}

// TestReclassifyQuotaSamples 覆盖历史样本的一次性重新归类。
//
// 样本把分类结果一起存了下来，所以仅改判定逻辑救不了老数据：
// 429 报文此前被记为 fatal、分数 0，而最新一条 fatal 会一票否决健康分 ——
// 这些渠道会一直卡在 0 分回不了池，即使上游早就恢复。
func TestReclassifyQuotaSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guardian.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}

	insert := func(id int64, msg string, status int) {
		t.Helper()
		if err := st.AddSample(domain.Sample{
			AccountID:  id,
			OccurredAt: time.Now(),
			Source:     domain.SourceProbe,
			EventType:  domain.EventFatal,
			Score:      0,
			StatusCode: status,
			RequestID:  msg[:8] + string(rune('a'+id)),
			Message:    msg,
		}); err != nil {
			t.Fatalf("写入样本失败: %v", err)
		}
	}

	insert(1, `API returned 429: {"error":{"type":"usage_limit_reached"}}`, 0)
	insert(2, "insufficient balance for this request", 0)
	insert(3, "429 Too Many Requests", 429)
	// Grok 的欠费报文：只含 "balance"，不含 usage limit / quota。
	// 第一版迁移在 SQL 里手抄关键字时漏了它，这些渠道继续卡在 0 分。
	insert(6, `Grok Responses API returned 402: {"error":"Grok Build usage balance exhausted"}`, 0)
	// 402 需付费，靠状态码也应识别。
	insert(7, "payment required", 402)
	// 这两条是真凭据失效，必须保持 fatal。
	insert(4, "401 Unauthorized: invalid api key", 401)
	insert(5, "forbidden: quota exceeded but also unauthorized", 403)

	// 把版本改回旧值，让迁移在下次打开时运行。
	if err := st.setMeta(metaSchemaVersion, "3"); err != nil {
		t.Fatalf("重置版本失败: %v", err)
	}
	_ = st.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	defer func() { _ = st2.Close() }()

	kinds := map[int64]domain.EventType{}
	scores := map[int64]float64{}
	rows, err := st2.db.Query(`SELECT account_id, event_type, score FROM samples`)
	if err != nil {
		t.Fatalf("读取样本失败: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var kind string
		var score float64
		if err := rows.Scan(&id, &kind, &score); err != nil {
			t.Fatalf("扫描失败: %v", err)
		}
		kinds[id] = domain.EventType(kind)
		scores[id] = score
	}

	for _, id := range []int64{1, 2, 3, 6, 7} {
		if kinds[id] != domain.EventQuotaExhausted {
			t.Fatalf("样本 %d 分类 = %v, 期望 quota_exhausted", id, kinds[id])
		}
		if scores[id] <= 0 {
			t.Fatalf("样本 %d 分数 = %.1f, 必须大于 0 否则渠道回不了池", id, scores[id])
		}
	}
	for _, id := range []int64{4, 5} {
		if kinds[id] != domain.EventFatal {
			t.Fatalf("样本 %d 是凭据失效，分类应保持 fatal，实际 %v", id, kinds[id])
		}
	}
}

// TestReclassifyRunsOnlyOnce 确认迁移不会每次启动都重跑。
func TestReclassifyRunsOnlyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guardian.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	_ = st.Close()

	// 版本已是当前值，再打开一次不应产生迁移事件。
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	defer func() { _ = st2.Close() }()

	events, _, err := st2.Events(EventFilter{Action: "migrate", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	for _, ev := range events {
		if strings.Contains(ev.Message, "重新归类") {
			t.Fatal("版本未变时不该重复执行样本重新归类")
		}
	}
}

// TestPolicyMigrationRespectsExplicitFalse 确认用户显式关掉的开关不会被改回来。
//
// 迁移按「字段在 JSON 里存在与否」判断，而不是看值 —— 否则用户主动关掉
// 只降级（想要熔断行为）会在每次重启时被悄悄改回 true。
func TestPolicyMigrationRespectsExplicitFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guardian.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}

	p, _ := st.Policy()
	p.Breaker.HTTPDegradeOnly = false // 用户显式关掉
	p.Breaker.LatencyDegradeOnly = false
	if _, err := st.SavePolicy(p); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}
	_ = st.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	defer func() { _ = st2.Close() }()

	got, err := st2.Policy()
	if err != nil {
		t.Fatalf("读取策略失败: %v", err)
	}
	if got.Breaker.HTTPDegradeOnly {
		t.Fatal("用户显式关掉的 http_degrade_only 不该被迁移改回 true")
	}
	if got.Breaker.LatencyDegradeOnly {
		t.Fatal("用户显式关掉的 latency_degrade_only 不该被迁移改回 true")
	}
}
