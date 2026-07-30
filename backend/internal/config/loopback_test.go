package config

import "testing"

// TestIsLoopbackAddr 覆盖「只听回环」的判定。
//
// 这个判定用于启动时提示。默认绑 127.0.0.1 在服务器上的表现是
// 「本机 curl 通、公网访问不到」，而日志里看不出原因 —— 很容易被误判为
// 防火墙或安全组问题，实际是进程压根没监听对外网卡。
func TestIsLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		// 只接受本机连接。
		"127.0.0.1:8787": true,
		"127.0.0.1":      true,
		"localhost:8787": true,
		"LocalHost:8787": true, // 大小写不敏感
		"127.5.6.7:8787": true, // 整个 127/8 都是回环
		"[::1]:8787":     true,
		"::1":            true,

		// 监听全部网卡，对外可访问。
		"0.0.0.0:8787":      false,
		":8787":             false, // 空主机等同全部网卡
		"[::]:8787":         false,
		"192.168.1.10:8787": false,
		"10.0.0.5:8787":     false,
		"example.com:8787":  false, // 域名不做解析，按非回环处理
	}

	for addr, want := range cases {
		t.Run(addr, func(t *testing.T) {
			if got := IsLoopbackAddr(addr); got != want {
				t.Fatalf("IsLoopbackAddr(%q) = %v, 期望 %v", addr, got, want)
			}
		})
	}
}

// TestDefaultAddrIsLoopback 固定「默认只听回环」这个刻意的选择。
//
// 面板持有 sub2api 的 Admin Key，能力等同管理员，默认不该暴露到公网。
// 这条测试的作用是：如果以后有人把默认值改成 0.0.0.0，会在这里失败，
// 迫使他确认这是有意的安全决策变更，而不是顺手改掉。
func TestDefaultAddrIsLoopback(t *testing.T) {
	t.Setenv("GUARDIAN_ADDR", "")
	t.Setenv("GUARDIAN_PORT", "")

	cfg := Load()
	if !IsLoopbackAddr(cfg.Addr) {
		t.Fatalf("默认监听地址 = %q，应为回环地址（面板持有 Admin Key，不该默认对公网开放）",
			cfg.Addr)
	}
}

// TestExplicitAddrOverridesDefault 确认环境变量能覆盖默认值。
func TestExplicitAddrOverridesDefault(t *testing.T) {
	t.Setenv("GUARDIAN_ADDR", "0.0.0.0:9000")

	cfg := Load()
	if cfg.Addr != "0.0.0.0:9000" {
		t.Fatalf("监听地址 = %q, 期望 0.0.0.0:9000", cfg.Addr)
	}
	if IsLoopbackAddr(cfg.Addr) {
		t.Fatal("0.0.0.0 不该被判为回环，否则启动提示会误报")
	}
}

// TestPortOnlyOverrideStaysLoopback 确认只改端口不会意外对外开放。
func TestPortOnlyOverrideStaysLoopback(t *testing.T) {
	t.Setenv("GUARDIAN_ADDR", "")
	t.Setenv("GUARDIAN_PORT", "9001")

	cfg := Load()
	if cfg.Addr != "127.0.0.1:9001" {
		t.Fatalf("监听地址 = %q, 期望 127.0.0.1:9001", cfg.Addr)
	}
}
