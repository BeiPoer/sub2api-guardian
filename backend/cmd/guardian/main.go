// Command guardian 启动 Sub2API Guardian：一个独立的渠道调度守护服务。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"sub2api-guardian/backend/internal/api"
	"sub2api-guardian/backend/internal/config"
	"sub2api-guardian/backend/internal/engine"
	"sub2api-guardian/backend/internal/store"
	"sub2api-guardian/backend/internal/upstream"
	"sub2api-guardian/backend/internal/web"
)

func main() {
	cfg := config.Load()
	if err := cfg.EnsureDataDir(); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}
	// 打印绝对路径：配置存在这个库里，一眼能确认用的是不是预期的那一个。
	log.Printf("数据目录: %s", cfg.DataDir)
	log.Printf("数据库:   %s", cfg.DBPath)

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer func() { _ = db.Close() }()

	conn, err := db.Connection()
	if err != nil {
		log.Fatalf("读取连接配置失败: %v", err)
	}
	// 环境变量只在提供时覆盖，页面上的配置仍然是最终来源。
	if cfg.BaseURL != "" {
		conn.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	}
	if cfg.AdminAPIKey != "" {
		conn.AdminAPIKey = cfg.AdminAPIKey
	}
	if err := db.SaveConnection(conn); err != nil {
		log.Fatalf("保存连接配置失败: %v", err)
	}

	client := upstream.New(conn.BaseURL, conn.AdminAPIKey, time.Duration(conn.TimeoutSeconds)*time.Second)
	eng := engine.New(db, client)
	eng.Start()
	defer eng.Stop()

	server := api.NewServer(db, client, eng, web.Handler())
	defer server.Close()

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		// SSE 需要长连接，因此不设置写超时。
	}

	go func() {
		log.Printf("Sub2API Guardian 已启动: http://%s", cfg.Addr)
		// 只听回环时明确说清楚。
		//
		// 默认绑 127.0.0.1 是有意的（面板持有 sub2api 的 Admin Key，等同管理员权限，
		// 不该默认暴露到公网）。但这个默认值在服务器上会表现为「本机 curl 通、
		// 公网访问不到」，而日志里只有一行 http://127.0.0.1:8787 —— 看不出问题在哪，
		// 很容易误以为是防火墙或安全组。
		if config.IsLoopbackAddr(cfg.Addr) {
			log.Printf("注意: 当前只监听回环地址，仅本机可访问；" +
				"公网/局域网访问会连接失败。需要远程访问请设 GUARDIAN_ADDR=0.0.0.0:8787")
			log.Printf("      直接暴露到公网前请务必先设好登录密码，" +
				"更稳妥的做法是保持回环 + 由 Nginx/Caddy 反代并启用 HTTPS")
		}
		if conn.BaseURL == "" || conn.AdminAPIKey == "" {
			log.Printf("提示: 尚未配置 sub2api 地址或 Admin API Key，请在页面「连接设置」里填写")
		}
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Printf("正在关闭...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("关闭 HTTP 服务失败: %v", err)
	}
}
