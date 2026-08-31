package policy

import (
	"math"
	"testing"
)

func TestResolveMultiplier(t *testing.T) {
	tests := []struct {
		name        string
		accountType string
		upstream    float64
		configure   func(*Policy)
		wantValue   float64
		wantSource  string
	}{
		{
			name:        "API Key 使用有效上游倍率",
			accountType: "apikey",
			upstream:    1.75,
			configure: func(p *Policy) {
				p.AccountUpstreamMultiplierEnabled["101"] = true
			},
			wantValue:  1.75,
			wantSource: MultiplierSourceUpstream,
		},
		{
			name:        "上游倍率为零时回退",
			accountType: "api_key",
			upstream:    0,
			configure: func(p *Policy) {
				p.AccountUpstreamMultiplierEnabled["101"] = true
			},
			wantValue:  DefaultAPIKeyMultiplier,
			wantSource: MultiplierSourceUpstreamFallback,
		},
		{
			name:        "上游倍率为 NaN 时回退",
			accountType: "key",
			upstream:    math.NaN(),
			configure: func(p *Policy) {
				p.AccountUpstreamMultiplierEnabled["101"] = true
			},
			wantValue:  DefaultAPIKeyMultiplier,
			wantSource: MultiplierSourceUpstreamFallback,
		},
		{
			name:        "上游倍率为无穷时回退",
			accountType: "apikey",
			upstream:    math.Inf(1),
			configure: func(p *Policy) {
				p.AccountUpstreamMultiplierEnabled["101"] = true
			},
			wantValue:  DefaultAPIKeyMultiplier,
			wantSource: MultiplierSourceUpstreamFallback,
		},
		{
			name:        "OAuth 忽略被篡改的开关",
			accountType: "oauth",
			upstream:    8,
			configure: func(p *Policy) {
				p.AccountUpstreamMultiplierEnabled["101"] = true
			},
			wantValue:  DefaultOAuthMultiplier,
			wantSource: MultiplierSourceDefault,
		},
		{
			name:        "关闭实时倍率后人工值优先",
			accountType: "apikey",
			upstream:    2,
			configure: func(p *Policy) {
				p.AccountMultipliers["101"] = 0.5
			},
			wantValue:  0.5,
			wantSource: MultiplierSourceManual,
		},
		{
			name:        "联动倍率覆盖实时与人工值",
			accountType: "apikey",
			upstream:    2,
			configure: func(p *Policy) {
				p.AccountMultipliers["101"] = 0.5
				p.AccountLinkedMultipliers["101"] = 0.12
				p.AccountUpstreamMultiplierEnabled["101"] = true
			},
			wantValue:  0.12,
			wantSource: MultiplierSourceLinked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Default()
			tt.configure(&p)
			gotValue, gotSource := p.ResolveMultiplier(101, tt.accountType, tt.upstream)
			if gotValue != tt.wantValue || gotSource != tt.wantSource {
				t.Fatalf("ResolveMultiplier() = %v/%s, 期望 %v/%s",
					gotValue, gotSource, tt.wantValue, tt.wantSource)
			}
		})
	}
}

func TestNormalizeRemovesInvalidMultipliers(t *testing.T) {
	p := Default()
	p.AccountMultipliers = map[string]float64{
		"zero": 0,
		"nan":  math.NaN(),
		"inf":  math.Inf(1),
		"ok":   1.25,
	}
	p.AccountLinkedMultipliers = map[string]float64{
		"bad": 0,
		"nan": math.NaN(),
		"ok":  0.12,
	}
	p.AccountUpstreamMultiplierEnabled = map[string]bool{"off": false, "on": true}
	p.AccountUpstreamMultiplierBreakers = map[string]UpstreamMultiplierBreaker{
		"off":     {Enabled: true, Threshold: 1.5},
		"on":      {Enabled: true, Threshold: 2.5},
		"invalid": {Enabled: true, Threshold: math.NaN()},
	}

	Normalize(&p)

	if len(p.AccountMultipliers) != 1 || p.AccountMultipliers["ok"] != 1.25 {
		t.Fatalf("非法倍率未清理: %#v", p.AccountMultipliers)
	}
	if len(p.AccountLinkedMultipliers) != 1 || p.AccountLinkedMultipliers["ok"] != 0.12 {
		t.Fatalf("非法联动倍率未清理: %#v", p.AccountLinkedMultipliers)
	}
	if len(p.AccountUpstreamMultiplierEnabled) != 1 || !p.AccountUpstreamMultiplierEnabled["on"] {
		t.Fatalf("关闭项未清理: %#v", p.AccountUpstreamMultiplierEnabled)
	}
	if len(p.AccountUpstreamMultiplierBreakers) != 1 ||
		p.AccountUpstreamMultiplierBreakers["on"].Threshold != 2.5 {
		t.Fatalf("非法或未启用渠道的阈值配置未清理: %#v", p.AccountUpstreamMultiplierBreakers)
	}
}

func TestUpstreamMultiplierIntervalDefaultsAndClamps(t *testing.T) {
	p := Policy{}
	Normalize(&p)
	if p.UpstreamMultiplier.IntervalSeconds != 120 {
		t.Fatalf("缺省倍率拉取周期 = %d，期望 120", p.UpstreamMultiplier.IntervalSeconds)
	}

	p.UpstreamMultiplier.IntervalSeconds = 5
	Normalize(&p)
	if p.UpstreamMultiplier.IntervalSeconds != 30 {
		t.Fatalf("过短倍率拉取周期 = %d，期望收敛到 30", p.UpstreamMultiplier.IntervalSeconds)
	}
}

func TestUpstreamMultiplierBreakerRequiresAutomaticFetch(t *testing.T) {
	p := Default()
	p.AccountUpstreamMultiplierBreakers["101"] = UpstreamMultiplierBreaker{
		Enabled: true, Threshold: 1.5,
	}
	if _, ok := p.UpstreamMultiplierBreakerFor(101, "apikey"); ok {
		t.Fatal("未开启实时倍率时阈值熔断不应生效")
	}

	p.AccountUpstreamMultiplierEnabled["101"] = true
	breaker, ok := p.UpstreamMultiplierBreakerFor(101, "apikey")
	if !ok || !breaker.Enabled || breaker.Threshold != 1.5 {
		t.Fatalf("已开启实时倍率却未返回阈值配置: %+v, ok=%v", breaker, ok)
	}
}

func TestResolveMultiplierSnapshot(t *testing.T) {
	p := Default()
	p.AccountMultipliers["101"] = 0.5
	p.AccountUpstreamMultiplierEnabled["101"] = true

	value, source := p.ResolveMultiplierSnapshot(101, "apikey", 1.75, 2.25, true)
	if value != 2.25 || source != MultiplierSourceUpstream {
		t.Fatalf("成功快照 = %v/%s", value, source)
	}

	value, source = p.ResolveMultiplierSnapshot(101, "apikey", 1.75, 0, false)
	if value != 1.75 || source != MultiplierSourceUpstreamFallback {
		t.Fatalf("无快照回退 = %v/%s", value, source)
	}

	value, source = p.ResolveMultiplierSnapshot(101, "apikey", 0, math.NaN(), true)
	if value != DefaultAPIKeyMultiplier || source != MultiplierSourceUpstreamFallback {
		t.Fatalf("非法快照回退 = %v/%s", value, source)
	}
}

func TestLinkedMultiplierIsHighestPriorityWithSnapshot(t *testing.T) {
	p := Default()
	p.AccountMultipliers["101"] = 0.5
	p.AccountLinkedMultipliers["101"] = 0.12
	p.AccountUpstreamMultiplierEnabled["101"] = true

	value, source := p.ResolveMultiplierSnapshot(101, "apikey", 1.75, 2.25, true)
	if value != 0.12 || source != MultiplierSourceLinked {
		t.Fatalf("联动倍率未覆盖快照 = %v/%s", value, source)
	}
}
