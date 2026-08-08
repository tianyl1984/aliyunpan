package webui

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tickstep/aliyunpan/internal/config"
)

const (
	sessionCookieName = "aliyunpan_webui_session"
	// defaultSessionTTL 会话有效期
	defaultSessionTTL = 7 * 24 * time.Hour
	// kdfIterations 口令派生迭代次数。单机自托管场景下取一个不影响体验的值
	kdfIterations = 120000
	// webuiConfigName webui 自身配置文件名
	webuiConfigName = "webui.json"
	// loginMaxFails 同一 IP 允许的连续失败次数
	loginMaxFails = 5
	// loginLockDuration 超过失败次数后的锁定时长
	loginLockDuration = 5 * time.Minute
)

// persistedConfig webui 自身的持久化配置（不含阿里云盘信息）
type persistedConfig struct {
	// Token 自动生成的访问 token。使用 --password 启动时该字段为空
	Token string `json:"token,omitempty"`
}

type failCounter struct {
	count      int
	lockedTill time.Time
}

// session 一次已认证的会话
type session struct {
	expire time.Time
	// user 登录者标识。token/口令登录为空，第三方认证服务登录时为服务返回的用户名
	user string
}

// authManager 负责 webui 自身的访问控制（与阿里云盘账号无关）
type authManager struct {
	mu sync.Mutex

	salt []byte
	hash []byte

	// plainToken 仅在自动生成 token 时非空，用于启动时打印给用户
	plainToken string

	sessions map[string]*session
	fails    map[string]*failCounter
	ttl      time.Duration
}

func newAuthManager(password string) (*authManager, error) {
	a := &authManager{
		sessions: make(map[string]*session),
		fails:    make(map[string]*failCounter),
		ttl:      defaultSessionTTL,
	}

	cred := password
	if cred == "" {
		// 未指定口令：复用或生成随机 token
		pc, err := loadPersistedConfig()
		if err != nil {
			return nil, err
		}
		if pc.Token == "" {
			t, err := randomHex(24)
			if err != nil {
				return nil, err
			}
			pc.Token = t
			if err := savePersistedConfig(pc); err != nil {
				return nil, err
			}
		}
		cred = pc.Token
		a.plainToken = pc.Token
	}

	salt, err := randomBytes(16)
	if err != nil {
		return nil, err
	}
	a.salt = salt
	a.hash = deriveKey(cred, salt)
	return a, nil
}

// deriveKey 迭代 HMAC-SHA256 的口令派生。只用标准库，避免为 webui 引入新的 go.mod 依赖。
func deriveKey(password string, salt []byte) []byte {
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write(salt)
	sum := mac.Sum(nil)
	for i := 1; i < kdfIterations; i++ {
		mac.Reset()
		mac.Write(sum)
		sum = mac.Sum(nil)
	}
	return sum
}

func (a *authManager) verifyCredential(cred string) bool {
	return subtle.ConstantTimeCompare(deriveKey(cred, a.salt), a.hash) == 1
}

// login 校验凭据并返回新会话 ID
func (a *authManager) login(remoteIP, cred string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	fc := a.fails[remoteIP]
	if fc != nil && now.Before(fc.lockedTill) {
		return "", newHTTPError(http.StatusTooManyRequests, http.StatusTooManyRequests,
			"失败次数过多，请于 "+fc.lockedTill.Format("15:04:05")+" 后重试")
	}

	if !a.verifyCredential(cred) {
		if fc == nil {
			fc = &failCounter{}
			a.fails[remoteIP] = fc
		}
		fc.count++
		if fc.count >= loginMaxFails {
			fc.lockedTill = now.Add(loginLockDuration)
			fc.count = 0
		}
		return "", newHTTPError(http.StatusUnauthorized, CodeUnauthorized, "口令错误")
	}

	delete(a.fails, remoteIP)
	return a.newSessionLocked(now, "")
}

// newSession 不校验凭据直接建会话，供身份已由外部认证服务确认的登录方式使用（见 auth_oauth.go）。
// 调用方必须自己完成身份校验，这里只负责发会话。
func (a *authManager) newSession(user string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.newSessionLocked(time.Now(), user)
}

func (a *authManager) newSessionLocked(now time.Time, user string) (string, error) {
	sid, err := randomHex(32)
	if err != nil {
		return "", internalError("生成会话失败: " + err.Error())
	}
	a.sessions[sid] = &session{expire: now.Add(a.ttl), user: user}
	a.gcLocked(now)
	return sid, nil
}

func (a *authManager) logout(sid string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, sid)
}

// lookup 返回会话，同时惰性清理已过期的会话
func (a *authManager) lookup(sid string) (*session, bool) {
	if sid == "" {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[sid]
	if !ok {
		return nil, false
	}
	if time.Now().After(s.expire) {
		delete(a.sessions, sid)
		return nil, false
	}
	return s, true
}

func (a *authManager) valid(sid string) bool {
	_, ok := a.lookup(sid)
	return ok
}

func (a *authManager) gcLocked(now time.Time) {
	for k, v := range a.sessions {
		if now.After(v.expire) {
			delete(a.sessions, k)
		}
	}
}

// ---- cookie 辅助 ----

func setSessionCookie(w http.ResponseWriter, sid string, secure bool, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

func sessionIDFromRequest(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- 持久化 ----

func webuiConfigPath() string {
	return filepath.Join(config.GetConfigDir(), webuiConfigName)
}

func loadPersistedConfig() (*persistedConfig, error) {
	pc := &persistedConfig{}
	b, err := os.ReadFile(webuiConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return pc, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return pc, nil
	}
	if err := json.Unmarshal(b, pc); err != nil {
		// 文件损坏时不阻塞启动，重新生成
		return &persistedConfig{}, nil
	}
	return pc, nil
}

func savePersistedConfig(pc *persistedConfig) error {
	b, err := json.MarshalIndent(pc, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(webuiConfigPath(), b, 0600)
}

// writeFileAtomic 临时文件 + 重命名的原子写
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

func randomHex(n int) (string, error) {
	b, err := randomBytes(n)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
