package webui

// 第三方登录：把身份校验委托给外部认证服务（cf-worker-auth）。
//
// 这是 token/口令登录之外的第二种登录方式，两者并存，互不影响：
// token 登录仍然可用，第三方登录只有在 --auth-url 指定了认证服务时才出现。
//
// 认证服务的契约（https://github.com/../cf-worker-auth）：
//   GET <base>/login?callback=<回调地址>   浏览器入口，用户授权后 302 跳回 <回调地址>?token=xxx
//   GET <base>/userinfo                    凭 token 换用户信息，token 有效期 10 分钟
//
// 两个必须照顾到的约束：
//  1. 认证服务回调时固定拼接 "?token=xxx"，回调地址自身不能再带 query，
//     所以本端的 state 只能以路径段的形式带出去（见 oauthCallbackPrefix）。
//  2. 回调地址的 host 必须预先加进认证服务 KV 的 legal_domain 白名单，否则 /login 直接报错。

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// oauthStateCookieName 第三方登录流程的 state 绑定 Cookie
	oauthStateCookieName = "aliyunpan_webui_oauth_state"
	// oauthStateTTL 一次第三方登录流程的有效期
	oauthStateTTL = 10 * time.Minute
	// oauthStartPath 前端跳转的第三方登录入口
	oauthStartPath = "/api/auth/oauth/start"
	// oauthCallbackPrefix 回调地址前缀，state 以路径段的形式跟在后面
	oauthCallbackPrefix = "/api/auth/oauth/callback/"
	// oauthMaxPendingStates 同时进行的登录流程上限，防止 state 无限堆积
	oauthMaxPendingStates = 512
	// oauthHTTPTimeout 访问认证服务的超时
	oauthHTTPTimeout = 15 * time.Second
)

// oauthProvider 登录按钮上显示的名字。cf-worker-auth 目前只对接 GitHub
const oauthProvider = "GitHub"

// oauthManager 第三方登录的状态与认证服务客户端
type oauthManager struct {
	// baseURL 认证服务地址，形如 https://auth.example.com（无尾部斜杠）
	baseURL string
	// allowUsers 允许登录的用户名（已小写）。为空表示完全信任认证服务自身的白名单
	allowUsers map[string]bool

	mu     sync.Mutex
	states map[string]*oauthState

	client *http.Client
}

type oauthState struct {
	expire time.Time
	// redirect 登录成功后跳回的站内路径，可为空
	redirect string
}

// newOAuthManager 未配置 --auth-url 时返回 (nil, nil)，表示不启用第三方登录
func newOAuthManager(opt *Options) (*oauthManager, error) {
	raw := strings.TrimSpace(opt.AuthURL)
	if raw == "" {
		return nil, nil
	}
	base, err := normalizeServiceURL(raw)
	if err != nil {
		return nil, fmt.Errorf("非法的认证服务地址 %q: %w", raw, err)
	}

	users := map[string]bool{}
	for _, u := range opt.AuthUsers {
		if s := strings.ToLower(strings.TrimSpace(u)); s != "" {
			users[s] = true
		}
	}

	return &oauthManager{
		baseURL:    base,
		allowUsers: users,
		states:     make(map[string]*oauthState),
		client:     &http.Client{Timeout: oauthHTTPTimeout},
	}, nil
}

// normalizeServiceURL 校验并规范化一个 http(s) 服务地址，输出 scheme://host[:port][/path]，无尾部斜杠
func normalizeServiceURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("应形如 https://auth.example.com")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("缺少 http:// 或 https:// 前缀")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("不应带 query 或 fragment")
	}
	return scheme + "://" + canonicalHost(scheme, u.Host) + strings.TrimSuffix(u.Path, "/"), nil
}

// normalizeExternalURL 规范化本服务的对外访问地址。
// 与认证服务地址不同，这里不接受子路径：SPA 与所有接口都挂在根路径上，
// 带路径前缀的对外地址拼出来的回调地址一定是错的，早点报错比让用户去猜 404 强。
func normalizeExternalURL(raw string) (string, error) {
	s, err := normalizeServiceURL(raw)
	if err != nil {
		return "", err
	}
	if i := strings.Index(s, "://"); strings.Contains(s[i+3:], "/") {
		return "", fmt.Errorf("不应带路径，应形如 https://pan.example.com")
	}
	return s, nil
}

// begin 开启一次登录流程，返回 state
func (o *oauthManager) begin(redirect string) (string, error) {
	state, err := randomHex(24)
	if err != nil {
		return "", internalError("生成 state 失败: " + err.Error())
	}
	now := time.Now()

	o.mu.Lock()
	defer o.mu.Unlock()
	for k, v := range o.states {
		if now.After(v.expire) {
			delete(o.states, k)
		}
	}
	if len(o.states) >= oauthMaxPendingStates {
		return "", newHTTPError(http.StatusTooManyRequests, http.StatusTooManyRequests, "待完成的登录请求过多，请稍后重试")
	}
	o.states[state] = &oauthState{expire: now.Add(oauthStateTTL), redirect: redirect}
	return state, nil
}

// consume 取出并删除 state，一个 state 只能用一次
func (o *oauthManager) consume(state string) (*oauthState, bool) {
	if state == "" {
		return nil, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	st, ok := o.states[state]
	if !ok {
		return nil, false
	}
	delete(o.states, state)
	if time.Now().After(st.expire) {
		return nil, false
	}
	return st, true
}

// loginURL 拼接认证服务的登录入口
func (o *oauthManager) loginURL(callback string) string {
	return o.baseURL + "/login?callback=" + url.QueryEscape(callback)
}

// allowed 本端的二次白名单。为空时完全信任认证服务
func (o *oauthManager) allowed(login string) bool {
	if len(o.allowUsers) == 0 {
		return true
	}
	return o.allowUsers[strings.ToLower(strings.TrimSpace(login))]
}

// oauthUserInfo 认证服务 /userinfo 的返回（GitHub /user 的原始 JSON，这里只取用得上的字段）
type oauthUserInfo struct {
	Login string `json:"login"`
	Name  string `json:"name"`
}

// fetchUser 用一次性 token 换取用户信息。
// token 走 Authorization 头而不是 query，避免落进反向代理的访问日志。
func (o *oauthManager) fetchUser(ctx context.Context, token string) (*oauthUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+"/userinfo", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("访问认证服务失败: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取认证服务响应失败: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("认证服务拒绝了本次登录 (%d): %s", resp.StatusCode, truncate(strings.TrimSpace(string(body)), 120))
	}

	info := &oauthUserInfo{}
	if err := json.Unmarshal(body, info); err != nil {
		return nil, fmt.Errorf("解析认证服务响应失败: %v", err)
	}
	if strings.TrimSpace(info.Login) == "" {
		return nil, fmt.Errorf("认证服务未返回用户名")
	}
	return info, nil
}

// ---- HTTP 处理 ----

// handleAuthOAuthStart 浏览器整页跳转的入口，302 到认证服务
func (s *Server) handleAuthOAuthStart(w http.ResponseWriter, r *http.Request) error {
	if s.oauth == nil {
		return notFound("未启用第三方登录")
	}
	if s.auth.valid(sessionIDFromRequest(r)) {
		http.Redirect(w, r, "/", http.StatusFound)
		return nil
	}

	base, err := s.externalBase(r)
	if err != nil {
		return err
	}
	state, err := s.oauth.begin(sanitizeRedirect(r.URL.Query().Get("redirect")))
	if err != nil {
		return err
	}
	setOAuthStateCookie(w, state, s.tlsEnabled())
	http.Redirect(w, r, s.oauth.loginURL(base+oauthCallbackPrefix+state), http.StatusFound)
	return nil
}

// handleAuthOAuthCallback 认证服务跳回来的落点：校验 state → 换用户信息 → 建会话。
// 这里是浏览器的整页导航，出错不能返回 JSON，一律跳回登录页并把原因带在 query 上。
func (s *Server) handleAuthOAuthCallback(w http.ResponseWriter, r *http.Request) error {
	if s.oauth == nil {
		return notFound("未启用第三方登录")
	}
	clearOAuthStateCookie(w, s.tlsEnabled())

	// state 双重校验：路径上的值必须与 Cookie 一致，且必须是本端发出且未使用过的
	state := r.PathValue("state")
	c, cErr := r.Cookie(oauthStateCookieName)
	if cErr != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		return oauthFail(w, r, "登录状态校验失败，请重新发起登录")
	}
	st, ok := s.oauth.consume(state)
	if !ok {
		return oauthFail(w, r, "登录请求无效或已过期，请重新发起登录")
	}

	token := r.URL.Query().Get("token")
	if token == "" {
		return oauthFail(w, r, "认证服务未返回 token")
	}

	ctx, cancel := context.WithTimeout(r.Context(), oauthHTTPTimeout)
	defer cancel()
	info, err := s.oauth.fetchUser(ctx, token)
	if err != nil {
		s.logf("第三方登录失败: %v", err)
		return oauthFail(w, r, err.Error())
	}
	if !s.oauth.allowed(info.Login) {
		s.logf("第三方登录被拒绝: 用户 %s 不在 --auth-user 白名单内", info.Login)
		return oauthFail(w, r, "用户 "+info.Login+" 未被授权访问本服务")
	}

	sid, err := s.auth.newSession(info.Login)
	if err != nil {
		return err
	}
	setSessionCookie(w, sid, s.tlsEnabled(), s.auth.ttl)
	s.logf("第三方登录成功: %s", info.Login)

	target := st.redirect
	if target == "" {
		target = "/"
	}
	http.Redirect(w, r, target, http.StatusFound)
	return nil
}

func oauthFail(w http.ResponseWriter, r *http.Request, msg string) error {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(truncate(msg, 200)), http.StatusFound)
	return nil
}

// externalBase 推导本服务的对外访问地址，用于拼接回调地址。
//
// 优先用 --external-url。没配就拿请求的 Host 试，但必须能通过 originAllowed
// （即指向本服务的监听地址，或已被 --trusted-origin 声明），否则伪造的 Host 头
// 就能把回调地址指到别处。TLS 由反代终止时监听侧是 http，所以额外试一次 https。
func (s *Server) externalBase(r *http.Request) (string, error) {
	if s.opt.ExternalURL != "" {
		return s.opt.ExternalURL, nil
	}
	host := r.Host
	if host == "" {
		return "", badRequest("请求缺少 Host 头，无法推导回调地址，请用 --external-url 指定对外访问地址")
	}
	for _, scheme := range []string{s.scheme(), "https"} {
		if s.originAllowed(&url.URL{Scheme: scheme, Host: host}) {
			return scheme + "://" + canonicalHost(scheme, host), nil
		}
	}
	return "", badRequest("无法确定本服务的对外访问地址（当前 Host: " + host +
		"）。请用 --external-url 指定，或用 --trusted-origin 声明该地址")
}

// sanitizeRedirect 只接受站内绝对路径，避免登录入口变成开放重定向
func sanitizeRedirect(p string) string {
	if p == "" || len(p) > 512 {
		return ""
	}
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return ""
	}
	if strings.ContainsAny(p, "\r\n") {
		return ""
	}
	return p
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---- state Cookie ----
//
// 必须是 Lax：认证服务是跨站 302 跳回来的，SameSite=Strict 的 Cookie 在这种
// 跨站顶级导航里不会被带上，state 就永远校验不过。

func setOAuthStateCookie(w http.ResponseWriter, state string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(oauthStateTTL.Seconds()),
	})
}

func clearOAuthStateCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
