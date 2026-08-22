// Package config 解析 Guardian 的启动配置。
package config

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"sub2api-guardian/backend/internal/fileperm"
)

// Config 是进程级配置，只能通过环境变量设置。
//
// 业务策略一律存在数据库里、由页面配置，这里只放启动期必需的少量参数。
type Config struct {
	Addr        string
	DataDir     string
	DBPath      string
	BaseURL     string // 首次启动时用于初始化连接配置
	AdminAPIKey string
}

// Load 从环境变量读取配置并填好默认值。
func Load() Config {
	dataDir := ResolveDataDir(os.Getenv("GUARDIAN_DATA_DIR"), os.Executable)
	cfg := Config{
		Addr:        env("GUARDIAN_ADDR", "127.0.0.1:"+env("GUARDIAN_PORT", "8787")),
		DataDir:     dataDir,
		DBPath:      filepath.Join(dataDir, "guardian.sqlite"),
		BaseURL:     strings.TrimSpace(os.Getenv("SUB2API_BASE_URL")),
		AdminAPIKey: strings.TrimSpace(os.Getenv("SUB2API_ADMIN_KEY")),
	}
	return cfg
}

// ResolveDataDir 决定数据目录，按优先级：
//
//  1. 显式指定的 GUARDIAN_DATA_DIR；
//  2. 可执行文件同级的 data/；
//  3. 当前工作目录下的 data/（兜底）。
//
// 第 2 条是关键。早期版本直接用相对路径 "data"，数据库便落在**启动进程时所在
// 的目录**下：换个目录启动、换个快捷方式、或者用工作目录不同的服务脚本拉起，
// 就会开出一个全新的空库，页面上看起来就像「配置每次重启都被重置」。
//
// exePath 作为参数注入，测试才能覆盖各分支。
func ResolveDataDir(explicit string, exePath func() (string, error)) string {
	if dir := strings.TrimSpace(explicit); dir != "" {
		return absOrRaw(dir)
	}
	if dir, ok := executableDir(exePath); ok {
		return absOrRaw(filepath.Join(dir, "data"))
	}
	return absOrRaw("data")
}

// executableDir 返回可执行文件所在目录，不可用时返回 false。
func executableDir(exePath func() (string, error)) (string, bool) {
	exe, err := exePath()
	if err != nil || strings.TrimSpace(exe) == "" {
		return "", false
	}
	// 符号链接要解开：/usr/local/bin/guardian -> /opt/guardian/guardian
	// 这种装法下，数据应该落在真实位置旁边而不是软链所在目录。
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	if isTempBuildDir(dir) {
		return "", false
	}
	return dir, true
}

// isTempBuildDir 判断路径是否为 `go run` 的临时构建目录。
//
// `go run`（即 make dev-backend）每次都把二进制编译到一个全新的 go-buildXXXX
// 目录。认它作数据目录的话，开发期每跑一次就是一个空库。这种情况退回相对路径，
// 让开发者仍能在项目目录里复用同一个库。
func isTempBuildDir(dir string) bool {
	for _, part := range strings.Split(filepath.ToSlash(dir), "/") {
		if strings.HasPrefix(part, "go-build") {
			return true
		}
	}
	return false
}

func absOrRaw(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// EnsureDataDir 创建数据目录。
func (c Config) EnsureDataDir() error {
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return err
	}
	return fileperm.Chmod(c.DataDir, 0o700)
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// EnvInt 读取整型环境变量，缺省或非法时返回 fallback。
func EnvInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return value
}

// IsLoopbackAddr 报告监听地址是否只接受本机回环连接。
//
// 用于启动时提示：默认绑 127.0.0.1 在服务器上会表现为「本机 curl 通、公网访问不到」，
// 而日志里看不出原因，很容易被误判为防火墙或安全组问题。
//
// 空主机（":8787"）与 0.0.0.0 / :: 都是监听全部网卡，不算回环。
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		// 没有端口部分时按整串当主机处理。
		host = strings.TrimSpace(addr)
	}
	if host == "" {
		return false
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
