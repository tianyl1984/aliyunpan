package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// distFS 内嵌的前端产物。
//
// 嵌入指令只能覆盖本包目录及其子目录，所以 web/vite.config.js 的 build.outDir
// 必须指向 internal/webui/assets/dist。
//
// 构建产物不进 git，仓库里只保留 assets/dist/.gitkeep 占位：
// 目录不存在或为空时 go build 会直接失败，有占位文件才能编译通过。
// 所以打包前必须先执行 ./build_web.sh，否则二进制里没有 index.html，
// 网页打开是空白（项目没有 CI，build.sh 直接调用 go build）。
//
//go:embed all:assets/dist
var distFS embed.FS

// staticHandler 返回 SPA 静态资源处理器。
// 任何未命中静态文件且不以 /api 开头的路径都回落到 index.html，交给前端路由。
func staticHandler() (http.Handler, error) {
	sub, err := fs.Sub(distFS, "assets/dist")
	if err != nil {
		return nil, err
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := strings.TrimPrefix(r.URL.Path, "/")
		if upath == "" {
			upath = "index.html"
		}

		if f, err := sub.Open(upath); err == nil {
			stat, statErr := f.Stat()
			f.Close()
			if statErr == nil && !stat.IsDir() {
				// Vite 产物带内容 hash，可以长缓存；index.html 必须每次校验
				if strings.HasPrefix(upath, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// SPA 回落
		serveIndex(w, r, sub)
	}), nil
}

func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "前端资源未构建，请先运行 ./build_web.sh", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
