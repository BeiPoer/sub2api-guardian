// Package web 托管内嵌的前端静态资源。
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist 是前端构建产物。目录不存在时用占位文件保证编译通过。
//
//go:embed all:dist
var dist embed.FS

// Handler 返回 SPA 静态资源处理器：命中文件直接返回，否则回落到 index.html。
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return placeholder()
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return placeholder()
	}

	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "" || clean == "." {
			serveIndex(w, sub)
			return
		}
		if _, err := fs.Stat(sub, clean); err != nil {
			// 前端路由由 SPA 自己处理，未命中的路径统一交给 index.html。
			serveIndex(w, sub)
			return
		}
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, sub fs.FS) {
	raw, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "前端资源缺失", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(raw)
}

func placeholder() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8">
<title>Sub2API Guardian</title>
<body style="font-family:system-ui;padding:48px;line-height:1.7">
<h1>Sub2API Guardian</h1>
<p>前端资源尚未构建。请先执行：</p>
<pre>cd sub2api-guardian/frontend
pnpm install
pnpm build</pre>
<p>随后重新编译后端即可在同一端口访问面板；开发期也可以直接用 <code>pnpm dev</code>。</p>
</body>`))
	})
}
