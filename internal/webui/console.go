package webui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tickstep/aliyunpan/cmder"
)

// consoleTimeout 单条命令的默认超时
const consoleTimeout = 300 * time.Second
const consoleMaxTimeout = 1800 * time.Second

// consoleDenylist 禁止在网页控制台执行的命令。
//
// 这些命令在服务进程里执行会有实际危害，而不是「不方便」：
//   - who    : internal/command/user_info.go 在未登录时会 os.Exit(1)，直接杀掉整个服务
//   - sync   : internal/command/sync.go 的停止逻辑是 for { fmt.Scan(&c) }，stdin 为 EOF 时空转打满 CPU
//   - login/su/drive : 需要 tty 或交互输入序号，会话会挂住；账号页已提供图形化替代
//   - quit/clear/history/env/update : 对常驻服务无意义或需要交互
//
// 这样处理之后，上面这些上游缺陷对 Web 端无害，fork 里就不必打本地补丁
// （本地补丁会成为后续每次 merge upstream 的冲突源）。
var consoleDenylist = map[string]string{
	"who":     "该命令在未登录时会终止进程，请使用账号页查看当前账号",
	"sync":    "同步备份需要常驻交互，请使用命令行运行",
	"login":   "请使用账号页的扫码登录",
	"su":      "请使用账号页切换账号",
	"drive":   "请使用账号页切换网盘",
	"quit":    "该命令对 Web 服务无意义",
	"exit":    "该命令对 Web 服务无意义",
	"clear":   "该命令对 Web 服务无意义",
	"history": "该命令对 Web 服务无意义",
	"env":     "请使用系统信息接口查看运行环境",
	"update":  "自动更新需要交互确认，请使用命令行运行",
}

// consoleAutoYes 这些命令会读 stdin 做二次确认，统一注入 -y 跳过
var consoleAutoYes = map[string]bool{
	"logout": true,
	"rename": true,
}

// Console 网页控制台。
//
// 复用 cmder.App()（main.go 里已经 SetApp 的那个单例），因此不需要把 main.go 的
// 命令组装抽成 BuildCommands()，对上游零改动。
type Console struct {
	mu         sync.Mutex
	allowShell bool
	events     *EventBroker
}

func NewConsole(events *EventBroker, allowShell bool) *Console {
	return &Console{events: events, allowShell: allowShell}
}

type consoleCommandDTO struct {
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases"`
	Usage    string   `json:"usage"`
	Category string   `json:"category"`
	Allowed  bool     `json:"allowed"`
	Reason   string   `json:"reason,omitempty"`
	Sub      []string `json:"sub,omitempty"`
}

func (c *Console) commands() []*consoleCommandDTO {
	app := cmder.App()
	if app == nil {
		return nil
	}
	out := make([]*consoleCommandDTO, 0, len(app.Commands))
	for _, cmd := range app.Commands {
		if cmd.Hidden {
			continue
		}
		d := &consoleCommandDTO{
			Name:     cmd.Name,
			Aliases:  cmd.Aliases,
			Usage:    cmd.Usage,
			Category: cmd.Category,
			Allowed:  true,
		}
		if reason, denied := c.denyReason(cmd.Name); denied {
			d.Allowed = false
			d.Reason = reason
		}
		for _, sub := range cmd.Subcommands {
			d.Sub = append(d.Sub, sub.Name)
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (c *Console) denyReason(name string) (string, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "run" {
		if c.allowShell {
			return "", false
		}
		return "执行任意系统命令已被禁用，需要以 --allow-shell 启动服务", true
	}
	if r, ok := consoleDenylist[name]; ok {
		return r, true
	}
	return "", false
}

// Exec 执行一条命令，输出实时写入 out。
//
// 命令层大量使用 fmt.Print* 直写 os.Stdout（cmdtable 也硬编码了 os.Stdout），
// 所以必须做进程级重定向，并用全局互斥保证同时只有一条命令在跑。
func (c *Console) Exec(ctx context.Context, argv []string, out io.Writer) (err error) {
	if len(argv) == 0 {
		return badRequest("argv 不能为空")
	}
	name := strings.ToLower(strings.TrimSpace(argv[0]))
	if name == "" {
		return badRequest("命令名不能为空")
	}
	if reason, denied := c.denyReason(name); denied {
		return forbidden(reason)
	}

	app := cmder.App()
	if app == nil {
		return internalError("命令行应用尚未初始化")
	}
	if app.Command(name) == nil {
		return badRequest("未知命令: " + name)
	}

	if consoleAutoYes[name] && !hasFlag(argv, "-y") {
		argv = append(argv, "-y")
	}

	c.mu.Lock()

	pr, pw, pipeErr := os.Pipe()
	if pipeErr != nil {
		c.mu.Unlock()
		return internalError("创建管道失败: " + pipeErr.Error())
	}
	// stdin 给一个已经关闭写端的管道：所有读立即 EOF，交互命令不会挂住
	stdinR, stdinW, sErr := os.Pipe()
	if sErr != nil {
		pr.Close()
		pw.Close()
		c.mu.Unlock()
		return internalError("创建管道失败: " + sErr.Error())
	}
	stdinW.Close()

	oldStdout, oldStderr, oldStdin := os.Stdout, os.Stderr, os.Stdin
	oldWriter, oldErrWriter := app.Writer, app.ErrWriter
	os.Stdout, os.Stderr, os.Stdin = pw, pw, stdinR
	app.Writer, app.ErrWriter = pw, pw

	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		buf := make([]byte, 4096)
		for {
			n, rErr := pr.Read(buf)
			if n > 0 {
				_, _ = out.Write(buf[:n])
			}
			if rErr != nil {
				return
			}
		}
	}()

	restore := func() {
		os.Stdout, os.Stderr, os.Stdin = oldStdout, oldStderr, oldStdin
		app.Writer, app.ErrWriter = oldWriter, oldErrWriter
		pw.Close()
		<-copyDone
		pr.Close()
		stdinR.Close()
		c.mu.Unlock()
	}

	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("命令执行发生 panic: %v", r)
			}
		}()
		done <- app.Run(append([]string{"aliyunpan"}, argv...))
	}()

	select {
	case runErr := <-done:
		restore()
		if runErr != nil {
			return newHTTPError(http.StatusOK, CodeUpstream, runErr.Error())
		}
		return nil

	case <-ctx.Done():
		// Go 无法强制终止一个 goroutine。这里只能停止等待并如实告知用户：
		// 命令仍在后台跑，标准输出还被它占用，所以控制台要等它自己结束才能再用。
		// 真正的防线是 denylist —— 会阻塞的命令根本不允许进来。
		go func() {
			<-done
			restore()
		}()
		return newHTTPError(http.StatusOK, CodeUpstream,
			"命令执行超时，仍在后台运行；在它结束前控制台不可用")
	}
}

func hasFlag(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

// ---- HTTP handlers ----

func (s *Server) handleConsoleCommands(w http.ResponseWriter, r *http.Request) error {
	return writeOKErr(w, map[string]interface{}{
		"commands":   s.console.commands(),
		"allowShell": s.opt.AllowShell,
	})
}

type consoleExecRequest struct {
	Argv       []string `json:"argv"`
	TimeoutSec int      `json:"timeoutSec"`
}

func (s *Server) handleConsoleExec(w http.ResponseWriter, r *http.Request) error {
	var req consoleExecRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if len(req.Argv) == 0 {
		return badRequest("argv 不能为空")
	}

	timeout := consoleTimeout
	if req.TimeoutSec > 0 {
		timeout = time.Duration(req.TimeoutSec) * time.Second
		if timeout > consoleMaxTimeout {
			timeout = consoleMaxTimeout
		}
	}

	sessionId, err := randomHex(8)
	if err != nil {
		return internalError("生成会话ID失败: " + err.Error())
	}

	// 立即返回，输出通过 SSE 推送
	writeOK(w, map[string]interface{}{"sessionId": sessionId, "argv": req.Argv})

	go func() {
		started := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		sink := &sseWriter{events: s.events, sessionId: sessionId}
		execErr := s.console.Exec(ctx, req.Argv, sink)
		sink.flush()

		msg := ""
		code := 0
		if execErr != nil {
			msg = execErr.Error()
			code = 1
		}
		s.events.Publish(Event{Type: EventConsoleExit, Data: map[string]interface{}{
			"sessionId":  sessionId,
			"code":       code,
			"message":    msg,
			"durationMs": time.Since(started).Milliseconds(),
		}})
	}()
	return nil
}

// sseWriter 把命令输出按块推成 SSE 事件
type sseWriter struct {
	events    *EventBroker
	sessionId string
	mu        sync.Mutex
	buf       strings.Builder
}

func (s *sseWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.buf.Write(p)
	shouldFlush := s.buf.Len() >= 2048 || strings.ContainsRune(string(p), '\n')
	s.mu.Unlock()
	if shouldFlush {
		s.flush()
	}
	return len(p), nil
}

func (s *sseWriter) flush() {
	s.mu.Lock()
	chunk := s.buf.String()
	s.buf.Reset()
	s.mu.Unlock()
	if chunk == "" {
		return
	}
	s.events.Publish(Event{Type: EventConsoleOutput, Data: map[string]interface{}{
		"sessionId": s.sessionId,
		"chunk":     chunk,
	}})
}
