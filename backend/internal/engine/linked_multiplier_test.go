package engine

import "testing"

func TestLinkedMultiplierNameIsIdempotent(t *testing.T) {
	tests := []struct {
		name  string
		ratio float64
		want  string
	}{
		{name: "渠道", ratio: 0.12, want: "渠道【x0.12】"},
		{name: "渠道【x0.5】", ratio: 0.12, want: "渠道【x0.12】"},
		{name: "渠道【x0.12】", ratio: 0.12, want: "渠道【x0.12】"},
		{name: "渠道【x格式异常", ratio: 2, want: "渠道【x2】"},
		{name: "渠道【x0.5】人工备注", ratio: 0.25, want: "渠道【x0.25】"},
		{name: "渠道 - 【x0.5】", ratio: 0.25, want: "渠道 - 【x0.25】"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := linkedMultiplierName(tt.name, tt.ratio); got != tt.want {
				t.Fatalf("linkedMultiplierName() = %q, 期望 %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeLinkedURLKeepsPathAndIgnoresCase(t *testing.T) {
	got, ok := normalizeLinkedURL("HTTPS://Example.COM/sub2api///")
	if !ok || got != "https://example.com/sub2api" {
		t.Fatalf("规范化 URL = %q, ok=%v", got, ok)
	}
}
