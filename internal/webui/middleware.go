package webui

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
)

// clientHeader 前端必须携带的自定义请求头。
// 简单请求带不了自定义头，浏览器会先发预检，跨站表单也就无法伪造写操作。
const clientHeader = "X-Aliyunpan-Client"

// checkCSRF 写操作的跨站防护。
//
// 三重保护中的后两重在这里：校验 Origin/Referer 与主机匹配，并要求自定义请求头。
// 第一重是会话 Cookie 的 SameSite=Strict（见 auth.go）。
func (s *Server) checkCSRF(r *http.Request) error {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}

	if r.Header.Get(clientHeader) == "" {
		return forbidden("缺少 " + clientHeader + " 请求头")
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		// 同源的 fetch 一定会带 Origin；没有就拒绝
		return forbidden("缺少 Origin/Referer")
	}
	u, err := url.Parse(origin)
	if err != nil {
		return forbidden("非法的 Origin")
	}
	if !s.originAllowed(u) {
		return forbidden("Origin 不匹配: " + origin)
	}
	return nil
}

// originAllowed 判断请求来源是否可信。
//
// 先匹配运维显式声明的可信来源（--trusted-origin），再回落到默认判断
// “来源必须指向本服务的监听地址”。两者是叠加关系而非替换：配了反代域名之后，
// 直接用 IP:端口 访问依然能用。
func (s *Server) originAllowed(u *url.URL) bool {
	if len(s.trustedOrigins) > 0 {
		scheme := strings.ToLower(u.Scheme)
		o := scheme + "://" + canonicalHost(scheme, u.Host)
		for _, t := range s.trustedOrigins {
			if o == t {
				return true
			}
		}
	}
	return s.hostAllowed(u.Scheme, u.Host)
}

// normalizeTrustedOrigins 校验并规范化可信来源列表。
//
// 输出统一为 scheme://host[:port] 的小写形式，并去掉协议默认端口，
// 与浏览器 Origin 头的写法对齐（浏览器发的 Origin 从不带 :80 / :443）。
// 任何一项非法都直接报错，宁可启动失败也不要静默地留一条永远匹配不上的规则。
func normalizeTrustedOrigins(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		// 通配符会让这层防护彻底失效，直接拒绝，不给"图省事"的口子
		if s == "*" {
			return nil, fmt.Errorf("可信来源不接受通配符 *，请逐个列出具体来源")
		}
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("非法的可信来源 %q，应形如 https://pan.example.com 或 http://192.168.1.10:9000", raw)
		}
		scheme := strings.ToLower(u.Scheme)
		if scheme != "http" && scheme != "https" {
			return nil, fmt.Errorf("可信来源 %q 缺少 http:// 或 https:// 前缀", raw)
		}
		o := scheme + "://" + canonicalHost(scheme, u.Host)
		if seen[o] {
			continue
		}
		seen[o] = true
		out = append(out, o)
	}
	return out, nil
}

// canonicalHost 小写化主机名并去掉协议默认端口
func canonicalHost(scheme, host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	switch scheme {
	case "https":
		h = strings.TrimSuffix(h, ":443")
	case "http":
		h = strings.TrimSuffix(h, ":80")
	}
	return h
}

// hostAllowed 判断请求来源是否指向本服务的监听地址。
//
// 端口必须对上：端口隔离是这层防护的核心价值。SameSite Cookie 不区分端口，
// 本机其他端口上的页面对浏览器来说属于同一站点，只有这里能把它们分开。
//
// Origin 省略端口时，按协议默认端口（http=80、https=443）补齐再比对。
// 早期版本在这里是"没写端口就跳过校验"，配合监听 0.0.0.0 时的放行分支，
// 会让任意 https:// 站点都通过 Origin 校验（另外两层防护仍在，故未被利用）。
// 反代或端口映射导致端口天然对不上时，用 --trusted-origin 显式声明，不要放宽这里。
func (s *Server) hostAllowed(scheme, host string) bool {
	if host == "" {
		return false
	}
	h, port := splitHostPort(host)
	if port == "" {
		port = defaultPortOf(scheme)
	}
	// 端口对不上，或协议未知导致端口无法推断，一律拒绝
	if port != strconv.Itoa(s.opt.Port) {
		return false
	}
	// 允许 localhost / 127.0.0.1 / ::1 互换
	switch strings.ToLower(strings.Trim(h, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return isLoopbackHost(s.opt.Host) || s.opt.Host == "0.0.0.0" || s.opt.Host == "::"
	}
	// 监听在 0.0.0.0 时无法预知用户用哪个 IP 访问，端口对上即认可
	if s.opt.Host == "0.0.0.0" || s.opt.Host == "::" {
		return true
	}
	return strings.EqualFold(h, s.opt.Host)
}

// splitHostPort 拆分 host[:port]，端口缺失时返回空串。
// 兼容 IPv6 的 [::1]:8080 写法。
func splitHostPort(host string) (string, string) {
	if h, p, err := net.SplitHostPort(host); err == nil {
		return h, p
	}
	return host, ""
}

// defaultPortOf 返回协议的默认端口，未知协议返回空串
func defaultPortOf(scheme string) string {
	switch strings.ToLower(scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

// recoverMiddleware 兜住任何 handler 里的 panic，避免打挂整个服务
func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logf("处理 %s %s 时 panic: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				writeErr(w, internalError(fmt.Sprintf("服务端内部错误: %v", rec)))
			}
		}()
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// ---- 认证接口 ----

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) error {
	return writeOKErr(w, map[string]interface{}{
		"authenticated": s.auth.valid(sessionIDFromRequest(r)),
		"clientHeader":  clientHeader,
	})
}

type authLoginRequest struct {
	Password string `json:"password"`
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) error {
	var req authLoginRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	sid, err := s.auth.login(remoteIP(r), req.Password)
	if err != nil {
		return err
	}
	setSessionCookie(w, sid, s.tlsEnabled(), s.auth.ttl)
	return writeOKErr(w, map[string]interface{}{"authenticated": true})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) error {
	s.auth.logout(sessionIDFromRequest(r))
	clearSessionCookie(w, s.tlsEnabled())
	return writeOKErr(w, map[string]interface{}{"authenticated": false})
}
