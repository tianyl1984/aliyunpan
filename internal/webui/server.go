package webui

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Server webui 的 HTTP 服务
type Server struct {
	opt  *Options
	auth *authManager
	// oauth 第三方登录。未配置 --auth-url 时为 nil，表示只保留 token/口令登录
	oauth      *oauthManager
	events     *EventBroker
	transfer   *Manager
	console    *Console
	uploads    *uploadSessionStore
	localRoots []string
	// trustedOrigins 规范化后的可信来源，形如 https://pan.example.com
	trustedOrigins []string

	// cfgMu 串行化所有对 config.Config 的写操作。
	// 这里不去给上游的 PanConfig 加锁（那是 fork 合并的高频冲突区），
	// 而是在 Web 层收敛写入口。已知的遗留竞态见 docs/webui_design.md。
	cfgMu sync.Mutex

	contentClient *http.Client
	httpSrv       *http.Server
	// logOut 服务启动时保存的真实 stdout。控制台执行期间 os.Stdout 会被重定向，
	// 服务自身的日志必须写这里，否则会混进命令输出。
	logOut io.Writer
}

func NewServer(opt *Options) (*Server, error) {
	auth, err := newAuthManager(opt.Password)
	if err != nil {
		return nil, err
	}

	roots := opt.LocalRoots
	if len(roots) == 0 {
		roots = defaultLocalRoots()
	} else {
		roots = normalizeRoots(roots)
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("没有可用的本地目录白名单，请通过 --local-root 指定")
	}

	// 可信来源有错就直接启动失败：留一条永远匹配不上的规则，
	// 用户只会看到莫名其妙的 403，比起不了服务更难排查
	trusted, err := normalizeTrustedOrigins(opt.TrustedOrigins)
	if err != nil {
		return nil, err
	}

	// 对外地址天然就是可信来源，自动并入，省得用户还要再写一遍 --trusted-origin
	if opt.ExternalURL != "" {
		ext, eErr := normalizeExternalURL(opt.ExternalURL)
		if eErr != nil {
			return nil, fmt.Errorf("非法的对外访问地址 %q: %w", opt.ExternalURL, eErr)
		}
		opt.ExternalURL = ext
		trusted, err = normalizeTrustedOrigins(append(trusted, ext))
		if err != nil {
			return nil, err
		}
	}

	oauth, err := newOAuthManager(opt)
	if err != nil {
		return nil, err
	}

	events := NewEventBroker()
	s := &Server{
		opt:            opt,
		auth:           auth,
		oauth:          oauth,
		events:         events,
		uploads:        newUploadSessionStore(),
		localRoots:     roots,
		trustedOrigins: trusted,
		contentClient:  newContentClient(),
		// 保存真实的 stdout：控制台执行命令期间 os.Stdout 会被重定向，
		// 服务自身的日志必须写这里，否则会混进命令输出被推给前端。
		logOut: os.Stdout,
	}
	s.transfer = NewManager(events, s.logf)
	s.console = NewConsole(events, opt.AllowShell)
	return s, nil
}

func (s *Server) logf(format string, a ...interface{}) {
	fmt.Fprintf(s.logOut, "[webui] "+format+"\n", a...)
}

func (s *Server) addr() string {
	return net.JoinHostPort(s.opt.Host, strconv.Itoa(s.opt.Port))
}

func (s *Server) tlsEnabled() bool {
	return s.opt.TLSCert != "" && s.opt.TLSKey != ""
}

func (s *Server) scheme() string {
	if s.tlsEnabled() {
		return "https"
	}
	return "http"
}

// Run 启动服务并阻塞，直到收到 SIGINT/SIGTERM
func (s *Server) Run() error {
	handler, err := s.buildHandler()
	if err != nil {
		return err
	}

	s.httpSrv = &http.Server{
		Addr:    s.addr(),
		Handler: handler,
		// 不设 WriteTimeout：SSE 是长连接，大文件流式下载也可能持续很久
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, lErr := net.Listen("tcp", s.addr())
	if lErr != nil {
		return fmt.Errorf("监听 %s 失败: %w", s.addr(), lErr)
	}

	s.printBanner()
	go s.stageGCLoop()

	errCh := make(chan error, 1)
	go func() {
		var e error
		if s.tlsEnabled() {
			e = s.httpSrv.ServeTLS(ln, s.opt.TLSCert, s.opt.TLSKey)
		} else {
			e = s.httpSrv.Serve(ln)
		}
		if e != nil && e != http.ErrServerClosed {
			errCh <- e
			return
		}
		errCh <- nil
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case e := <-errCh:
		return e
	case sig := <-sigCh:
		s.logf("收到信号 %s，正在关闭...", sig)
	}

	// 先取消传输任务，让断点信息落盘，再关 HTTP
	s.transfer.Shutdown(10 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e := s.httpSrv.Shutdown(ctx); e != nil {
		s.logf("关闭 HTTP 服务出错: %v", e)
	}
	s.logf("已退出")
	return nil
}

func (s *Server) printBanner() {
	url := fmt.Sprintf("%s://%s", s.scheme(), s.addr())
	if s.opt.Host == "0.0.0.0" || s.opt.Host == "::" {
		url = fmt.Sprintf("%s://<本机IP>:%d", s.scheme(), s.opt.Port)
	}
	fmt.Fprintln(s.logOut, "")
	fmt.Fprintln(s.logOut, "  aliyunpan Web 管理界面已启动")
	fmt.Fprintln(s.logOut, "  地址: "+url)
	if s.auth.plainToken != "" {
		fmt.Fprintln(s.logOut, "  访问 token: "+s.auth.plainToken)
		fmt.Fprintln(s.logOut, "  (已保存到 "+webuiConfigPath()+"，下次启动复用)")
	} else {
		fmt.Fprintln(s.logOut, "  访问口令: 使用 --password 指定的口令")
	}
	if s.oauth != nil {
		fmt.Fprintln(s.logOut, "  第三方登录: 已启用，认证服务 "+s.oauth.baseURL)
		if s.opt.ExternalURL != "" {
			fmt.Fprintln(s.logOut, "  回调地址: "+s.opt.ExternalURL+oauthCallbackPrefix+"<state>")
			fmt.Fprintln(s.logOut, "  (请确认该域名已加入认证服务的 legal_domain 白名单)")
		} else {
			fmt.Fprintln(s.logOut, "  回调地址: 从请求 Host 推导。经反向代理访问时建议用 --external-url 显式指定")
		}
		if len(s.oauth.allowUsers) > 0 {
			fmt.Fprintln(s.logOut, "  允许登录的用户: "+strings.Join(sortedKeys(s.oauth.allowUsers), ", "))
		}
	}
	if len(s.trustedOrigins) > 0 {
		fmt.Fprintln(s.logOut, "  可信来源: "+strings.Join(s.trustedOrigins, ", "))
	}
	if !isLoopbackHost(s.opt.Host) {
		fmt.Fprintln(s.logOut, "  警告: 正在监听非回环地址，请确保处于可信网络或已配置 TLS/反向代理")
	}
	if s.opt.AllowShell {
		fmt.Fprintln(s.logOut, "  警告: 已允许网页控制台执行 run 命令（可执行任意系统命令）")
	}
	fmt.Fprintln(s.logOut, "  按 Ctrl+C 退出")
	fmt.Fprintln(s.logOut, "")
}

// stageGCLoop 定期清理浏览器直传遗留的暂存文件
func (s *Server) stageGCLoop() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		s.uploads.gcExpired()
	}
}
