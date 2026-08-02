package webui

import (
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tickstep/aliyunpan-api/aliyunpan"
	"github.com/tickstep/aliyunpan/internal/config"
	"github.com/tickstep/library-go/logger"
)

// previewMIMEWhitelist 允许 inline 预览的类型前缀。
// 白名单之外一律按附件下载，避免在同源页面里渲染任意内容。
var previewMIMEWhitelist = []string{
	"image/", "video/", "audio/", "text/plain", "application/pdf",
}

// handleFileContent 流式回传网盘文件。
//
// 这里由服务端代理转发，不做 302 重定向：阿里云盘的直链有效期极短且对
// Referer / User-Agent 有要求，直接交给浏览器容易 403；代理转发还能避免把
// access token 暴露给前端。
func (s *Server) handleFileContent(w http.ResponseWriter, r *http.Request) error {
	return s.serveFileContent(w, r, false)
}

// handleFilePreview 与 content 相同，但对白名单类型使用 inline 展示
func (s *Server) handleFilePreview(w http.ResponseWriter, r *http.Request) error {
	return s.serveFileContent(w, r, true)
}

func (s *Server) serveFileContent(w http.ResponseWriter, r *http.Request, inline bool) error {
	u, err := activeUser()
	if err != nil {
		return err
	}
	q := r.URL.Query()
	driveId, err := resolveDriveId(u, q.Get("driveId"))
	if err != nil {
		return err
	}

	fe, err := resolveTargetFile(u, driveId, q.Get("fileId"), q.Get("path"))
	if err != nil {
		return err
	}
	if fe.IsFolder() {
		return badRequest("不能下载目录，请使用传输任务")
	}

	dl, apiErr := u.PanClient().OpenapiPanClient().GetFileDownloadUrl(&aliyunpan.GetFileDownloadUrlParam{
		DriveId:   driveId,
		FileId:    fe.FileId,
		ExpireSec: 14400,
	})
	if apiErr != nil {
		return upstreamError(apiErr)
	}
	if dl == nil || dl.Url == "" {
		return internalError("获取下载链接失败")
	}

	req, reqErr := http.NewRequestWithContext(r.Context(), http.MethodGet, dl.Url, nil)
	if reqErr != nil {
		return internalError("构造上游请求失败: " + reqErr.Error())
	}
	// 透传 Range，浏览器的拖动进度条 / 断点续传才能工作
	if rng := r.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}
	req.Header.Set("Referer", "https://www.alipan.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; aliyunpan-webui)")

	resp, doErr := s.contentClient.Do(req)
	if doErr != nil {
		return internalError("请求上游文件失败: " + doErr.Error())
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return newHTTPError(http.StatusBadGateway, CodeUpstream,
			"上游返回 "+strconv.Itoa(resp.StatusCode))
	}

	ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(fe.FileName)))
	if ct == "" {
		ct = "application/octet-stream"
	}
	disposition := "attachment"
	if inline && isPreviewable(ct) {
		disposition = "inline"
	}

	h := w.Header()
	for _, k := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	if h.Get("Accept-Ranges") == "" {
		h.Set("Accept-Ranges", "bytes")
	}
	h.Set("Content-Type", ct)
	h.Set("Content-Disposition", disposition+"; filename*=UTF-8''"+url.PathEscape(fe.FileName))
	h.Set("X-Content-Type-Options", "nosniff")

	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return nil
	}
	if _, cErr := io.Copy(w, resp.Body); cErr != nil {
		// 客户端提前断开是常态，只记录不当作错误
		logger.Verboseln("webui stream file interrupted: ", cErr)
	}
	return nil
}

func isPreviewable(ct string) bool {
	for _, p := range previewMIMEWhitelist {
		if strings.HasPrefix(ct, p) {
			return true
		}
	}
	return false
}

// handleFileThumbnail 转发缩略图。相册文件的 Thumbnail 字段直接可用。
func (s *Server) handleFileThumbnail(w http.ResponseWriter, r *http.Request) error {
	u, err := activeUser()
	if err != nil {
		return err
	}
	q := r.URL.Query()
	driveId, err := resolveDriveId(u, q.Get("driveId"))
	if err != nil {
		return err
	}
	fe, err := resolveTargetFile(u, driveId, q.Get("fileId"), q.Get("path"))
	if err != nil {
		return err
	}
	if fe.Thumbnail == "" {
		return notFound("该文件没有缩略图")
	}

	req, reqErr := http.NewRequestWithContext(r.Context(), http.MethodGet, fe.Thumbnail, nil)
	if reqErr != nil {
		return internalError(reqErr.Error())
	}
	req.Header.Set("Referer", "https://www.alipan.com/")
	resp, doErr := s.contentClient.Do(req)
	if doErr != nil {
		return internalError("请求缩略图失败: " + doErr.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return notFound("缩略图不可用")
	}

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
	return nil
}

func resolveTargetFile(u *config.PanUser, driveId, fileId, panPath string) (*aliyunpan.FileEntity, error) {
	if id := strings.TrimSpace(fileId); id != "" {
		fe, apiErr := u.PanClient().OpenapiPanClient().FileInfoById(driveId, id)
		if apiErr != nil {
			return nil, upstreamError(apiErr)
		}
		if fe == nil {
			return nil, notFound("文件不存在")
		}
		return fe, nil
	}
	if strings.TrimSpace(panPath) == "" {
		return nil, badRequest("必须提供 path 或 fileId")
	}
	return lookupFile(u, driveId, panPath)
}

// newContentClient 用于代理转发的 HTTP 客户端。
// 不设总超时：大文件流式传输可能持续很久，只限制建连和响应头阶段。
func newContentClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          32,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			ExpectContinueTimeout: 5 * time.Second,
		},
	}
}
