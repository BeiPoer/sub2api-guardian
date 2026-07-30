package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fakeExe(path string) func() (string, error) {
	return func() (string, error) { return path, nil }
}

// TestResolveDataDirPrefersExplicit 确认显式配置优先级最高。
func TestResolveDataDirPrefersExplicit(t *testing.T) {
	got := ResolveDataDir("  /srv/guardian-data  ", fakeExe("/opt/app/guardian"))
	want, _ := filepath.Abs("/srv/guardian-data")
	if got != want {
		t.Fatalf("数据目录 = %q, 期望 %q（GUARDIAN_DATA_DIR 应最优先）", got, want)
	}
}

// TestResolveDataDirUsesExecutableDir 是这次修复的核心回归。
//
// 数据目录必须由可执行文件位置决定，与启动时的工作目录无关 ——
// 否则换个目录启动就会开一个新的空库，配置看起来像被重置了。
func TestResolveDataDirUsesExecutableDir(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "guardian.exe")
	got := ResolveDataDir("", fakeExe(exe))
	want := filepath.Join(filepath.Dir(exe), "data")
	if got != want {
		t.Fatalf("数据目录 = %q, 期望 %q（应取可执行文件同级）", got, want)
	}
}

// TestResolveDataDirIgnoresGoRunTempDir 覆盖 `go run` 的临时构建目录。
//
// go run 每次编译到全新的 go-buildXXXX 目录，认它作数据目录的话，
// 开发期每跑一次 make dev-backend 都是一个空库。
func TestResolveDataDirIgnoresGoRunTempDir(t *testing.T) {
	exe := filepath.FromSlash("/tmp/go-build1045225616/b001/exe/guardian")
	got := ResolveDataDir("", fakeExe(exe))
	fallback, _ := filepath.Abs("data")
	if got != fallback {
		t.Fatalf("数据目录 = %q, 期望回退到 %q（go run 临时目录不能作数据目录）", got, fallback)
	}
	if strings.Contains(filepath.ToSlash(got), "go-build") {
		t.Fatal("数据目录不该落在 go-build 临时目录里")
	}
}

// TestResolveDataDirFallsBackWhenExecutableUnavailable 覆盖取不到可执行路径的情况。
func TestResolveDataDirFallsBackWhenExecutableUnavailable(t *testing.T) {
	broken := func() (string, error) { return "", errors.New("不支持") }
	got := ResolveDataDir("", broken)
	want, _ := filepath.Abs("data")
	if got != want {
		t.Fatalf("数据目录 = %q, 期望 %q", got, want)
	}
}

// TestResolveDataDirIsAbsolute 确认结果总是绝对路径，日志里能直接看出库在哪。
func TestResolveDataDirIsAbsolute(t *testing.T) {
	cases := map[string]string{
		"显式相对路径":  "mydata",
		"可执行文件同级": "",
	}
	for name, explicit := range cases {
		t.Run(name, func(t *testing.T) {
			got := ResolveDataDir(explicit, fakeExe(filepath.Join(t.TempDir(), "guardian")))
			if !filepath.IsAbs(got) {
				t.Fatalf("数据目录 %q 不是绝对路径", got)
			}
		})
	}
}

func TestEnsureDataDirUsesPrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX directory permission bits")
	}
	dir := filepath.Join(t.TempDir(), "guardian-data")
	if err := (Config{DataDir: dir}).EnsureDataDir(); err != nil {
		t.Fatalf("创建数据目录失败: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("读取数据目录权限失败: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("数据目录权限 = %o，期望 700", got)
	}
}
