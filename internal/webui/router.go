package webui

import (
	"net/http"
	"strings"
)

// apiHandler 返回 error 的处理器签名，由 wrap 统一转成 JSON 响应
type apiHandler func(http.ResponseWriter, *http.Request) error

func (s *Server) buildHandler() (http.Handler, error) {
	mux := http.NewServeMux()

	// ---- 认证（无需已登录） ----
	mux.Handle("GET /api/auth/status", s.public(s.handleAuthStatus))
	mux.Handle("POST /api/auth/login", s.public(s.handleAuthLogin))
	mux.Handle("POST /api/auth/logout", s.public(s.handleAuthLogout))

	// ---- 账号与配置 ----
	mux.Handle("GET /api/account/current", s.guard(s.handleAccountCurrent))
	mux.Handle("GET /api/account/list", s.guard(s.handleAccountList))
	mux.Handle("GET /api/account/drives", s.guard(s.handleAccountDrives))
	mux.Handle("GET /api/account/quota", s.guard(s.handleAccountQuota))
	mux.Handle("POST /api/account/switch", s.guard(s.handleAccountSwitch))
	mux.Handle("POST /api/account/drive/switch", s.guard(s.handleDriveSwitch))
	mux.Handle("DELETE /api/account/{userId}", s.guard(s.handleAccountDelete))
	mux.Handle("POST /api/account/oauth/start", s.guard(s.handleOAuthStart))
	mux.Handle("GET /api/account/oauth/poll", s.guard(s.handleOAuthPoll))

	mux.Handle("GET /api/config", s.guard(s.handleConfigGet))
	mux.Handle("PUT /api/config", s.guard(s.handleConfigUpdate))
	mux.Handle("GET /api/system/info", s.guard(s.handleSystemInfo))

	// ---- 文件管理 ----
	mux.Handle("GET /api/files", s.guard(s.handleFileList))
	mux.Handle("GET /api/files/info", s.guard(s.handleFileInfo))
	mux.Handle("GET /api/files/search", s.guard(s.handleFileSearch))
	mux.Handle("POST /api/files/mkdir", s.guard(s.handleFileMkdir))
	mux.Handle("POST /api/files/delete", s.guard(s.handleFileDelete))
	mux.Handle("POST /api/files/copy", s.guard(s.handleFileCopy))
	mux.Handle("POST /api/files/move", s.guard(s.handleFileMove))
	mux.Handle("POST /api/files/rename", s.guard(s.handleFileRename))
	mux.Handle("GET /api/files/content", s.guard(s.handleFileContent))
	mux.Handle("HEAD /api/files/content", s.guard(s.handleFileContent))
	mux.Handle("GET /api/files/preview", s.guard(s.handleFilePreview))
	mux.Handle("GET /api/files/thumbnail", s.guard(s.handleFileThumbnail))

	// ---- 服务器本地文件 ----
	mux.Handle("GET /api/local/roots", s.guard(s.handleLocalRoots))
	mux.Handle("GET /api/local/ls", s.guard(s.handleLocalList))

	// ---- 传输 ----
	mux.Handle("GET /api/transfer/jobs", s.guard(s.handleTransferJobs))
	mux.Handle("GET /api/transfer/jobs/{id}", s.guard(s.handleTransferJobGet))
	mux.Handle("POST /api/transfer/download", s.guard(s.handleTransferDownload))
	mux.Handle("POST /api/transfer/upload", s.guard(s.handleTransferUpload))
	mux.Handle("POST /api/transfer/jobs/{id}/pause", s.guard(s.handleJobPause))
	mux.Handle("POST /api/transfer/jobs/{id}/resume", s.guard(s.handleJobResume))
	mux.Handle("POST /api/transfer/jobs/{id}/cancel", s.guard(s.handleJobCancel))
	mux.Handle("POST /api/transfer/jobs/{id}/retry", s.guard(s.handleJobRetry))
	mux.Handle("DELETE /api/transfer/jobs/{id}", s.guard(s.handleJobDelete))
	mux.Handle("POST /api/transfer/clear", s.guard(s.handleTransferClear))

	// ---- 浏览器直传 ----
	mux.Handle("POST /api/upload/session", s.guard(s.handleUploadSessionCreate))
	mux.Handle("PUT /api/upload/session/{id}/chunk", s.guard(s.handleUploadSessionChunk))
	mux.Handle("POST /api/upload/session/{id}/complete", s.guard(s.handleUploadSessionComplete))
	mux.Handle("DELETE /api/upload/session/{id}", s.guard(s.handleUploadSessionDelete))

	// ---- 事件与控制台 ----
	mux.Handle("GET /api/events", s.guard(s.handleEvents))
	mux.Handle("GET /api/console/commands", s.guard(s.handleConsoleCommands))
	mux.Handle("POST /api/console/exec", s.guard(s.handleConsoleExec))

	// ---- 静态资源 ----
	static, err := staticHandler()
	if err != nil {
		return nil, err
	}
	mux.Handle("/", s.spa(static))

	return s.recoverMiddleware(mux), nil
}

// public 不需要认证的接口，但仍然做 CSRF 与 panic 防护
func (s *Server) public(h apiHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.checkCSRF(r); err != nil {
			writeErr(w, err)
			return
		}
		if err := h(w, r); err != nil {
			writeErr(w, err)
		}
	})
}

// guard 需要认证的接口
func (s *Server) guard(h apiHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.checkCSRF(r); err != nil {
			writeErr(w, err)
			return
		}
		if !s.auth.valid(sessionIDFromRequest(r)) {
			writeErr(w, ErrUnauthorized)
			return
		}
		if err := h(w, r); err != nil {
			writeErr(w, err)
		}
	})
}

// spa 静态资源。/api 前缀下未匹配到的路由返回 JSON 404，避免把 index.html 当接口响应返回。
func (s *Server) spa(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeErr(w, notFound("接口不存在: "+r.URL.Path))
			return
		}
		next.ServeHTTP(w, r)
	})
}
