package webui

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/tickstep/aliyunpan-api/aliyunpan/apierror"
)

// 业务错误码
const (
	CodeOK           = 0
	CodeBadRequest   = 400
	CodeUnauthorized = 401
	CodeForbidden    = 403
	CodeNotFound     = 404
	CodeConflict     = 409
	CodeInternal     = 500
	CodeNotLogin     = 1001 // 阿里云盘账号未登录（区别于 webui 自身未认证）
	CodeUpstream     = 1002 // 阿里云盘接口返回错误
)

type apiResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// httpError 携带 HTTP 状态码与业务错误码的错误
type httpError struct {
	Status  int
	Code    int
	Message string
}

func (e *httpError) Error() string { return e.Message }

func newHTTPError(status, code int, msg string) *httpError {
	return &httpError{Status: status, Code: code, Message: msg}
}

var (
	// ErrNotLogin 阿里云盘账号未登录
	ErrNotLogin = newHTTPError(http.StatusOK, CodeNotLogin, "未登录阿里云盘账号，请先在账号页扫码登录")
	// ErrUnauthorized webui 自身未认证
	ErrUnauthorized = newHTTPError(http.StatusUnauthorized, CodeUnauthorized, "未认证或会话已过期")
)

func badRequest(msg string) *httpError {
	return newHTTPError(http.StatusBadRequest, CodeBadRequest, msg)
}

func notFound(msg string) *httpError {
	return newHTTPError(http.StatusNotFound, CodeNotFound, msg)
}

func forbidden(msg string) *httpError {
	return newHTTPError(http.StatusForbidden, CodeForbidden, msg)
}

func internalError(msg string) *httpError {
	return newHTTPError(http.StatusInternalServerError, CodeInternal, msg)
}

// upstreamError 把阿里云盘 API 错误包装成 httpError
func upstreamError(err *apierror.ApiError) *httpError {
	if err == nil {
		return nil
	}
	return newHTTPError(http.StatusOK, CodeUpstream, err.Error())
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOK(w http.ResponseWriter, data interface{}) {
	writeJSON(w, http.StatusOK, &apiResponse{Code: CodeOK, Message: "ok", Data: data})
}

func writeErr(w http.ResponseWriter, err error) {
	var he *httpError
	if errors.As(err, &he) {
		writeJSON(w, he.Status, &apiResponse{Code: he.Code, Message: he.Message})
		return
	}
	writeJSON(w, http.StatusInternalServerError, &apiResponse{Code: CodeInternal, Message: err.Error()})
}

// decodeJSON 解析请求体，限制最大 4MB 防止内存滥用
func decodeJSON(r *http.Request, v interface{}) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("请求体解析失败: " + err.Error())
	}
	return nil
}
