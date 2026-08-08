// Package webui 为 aliyunpan 提供内嵌的 Web 管理界面。
//
// 本包是 fork 专属代码，上游 tickstep/aliyunpan 不存在此目录。
// 设计原则：所有 Web 相关逻辑都收敛在本包内，对上游文件的改动只有 main.go 的一行注册语句，
// 以便长期 `git merge upstream/main` 不产生冲突。详见 docs/webui_design.md。
package webui

import (
	"fmt"
	"os"
	"strings"

	"github.com/tickstep/aliyunpan/internal/config"
	"github.com/urfave/cli"
)

const (
	// DefaultPort 默认监听端口
	DefaultPort = 8080
	// DefaultHost 默认监听地址，只监听回环地址
	DefaultHost = "127.0.0.1"
)

// Options webui 命令的启动参数
type Options struct {
	Host string
	Port int
	// Password 访问口令。为空时自动生成随机 token 并持久化到 <configDir>/webui.json
	Password string
	// LocalRoots 允许通过 Web 浏览的服务器本地根目录，为空时使用默认值（家目录 + 默认下载目录）
	LocalRoots []string
	// AllowShell 是否允许在网页控制台执行 run 命令（执行任意系统命令）
	AllowShell bool
	// TrustedOrigins 额外信任的浏览器来源，形如 https://pan.example.com。
	// 反向代理或 Docker 端口映射会让浏览器 Origin 与本服务监听地址不一致，
	// 此时必须显式声明，否则写操作会被 CSRF 防护拦下。
	TrustedOrigins []string
	// TLSCert / TLSKey 可选的 TLS 证书
	TLSCert string
	TLSKey  string

	// AuthURL 第三方认证服务（cf-worker-auth）地址，形如 https://auth.example.com。
	// 设置后登录页会多出一个第三方登录入口；token/口令登录始终保留，两种方式并存。
	AuthURL string
	// AuthUsers 允许通过第三方登录的用户名白名单（GitHub login）。
	// 为空表示完全信任认证服务自身的 legal_user 白名单。
	AuthUsers []string
	// ExternalURL 本服务的对外访问地址，形如 https://pan.example.com，
	// 用于拼接第三方登录的回调地址。不设置时从请求的 Host 推导。
	// 设置后会自动并入 TrustedOrigins。
	ExternalURL string
}

// CmdWebUI 返回 webui 命令定义
func CmdWebUI() cli.Command {
	return cli.Command{
		Name:  "webui",
		Usage: "启动 Web 管理界面",
		Description: `
	在本机启动一个内嵌的 Web 服务，用浏览器管理阿里云盘。

	示例:
		1. 默认只监听回环地址，端口 8080，首次启动自动生成访问 token 并打印
		aliyunpan webui

		2. 指定端口和访问口令
		aliyunpan webui --port 9000 --password mypassword

		3. 允许局域网访问（必须同时设置口令，否则拒绝启动）
		aliyunpan webui --host 0.0.0.0 --password mypassword

		4. 允许网页控制台执行 run 命令（执行任意系统命令，有风险）
		aliyunpan webui --allow-shell

		5. 放在反向代理后面，或宿主端口与监听端口不一致时，声明可信来源
		aliyunpan webui --host 0.0.0.0 --password mypassword \
			--trusted-origin https://pan.example.com \
			--trusted-origin http://192.168.1.10:9000

		6. 接入第三方认证服务（cf-worker-auth），登录页多出「使用 GitHub 登录」按钮
		aliyunpan webui --host 0.0.0.0 \
			--external-url https://pan.example.com \
			--auth-url https://auth.example.com \
			--auth-user your-github-id

	登录方式:
		两种方式并存，任选其一即可登录。
		1. token / 口令：--password 指定的口令，或首次启动自动生成并打印的 token。
		2. 第三方登录：配置 --auth-url 后启用，身份由认证服务校验。
		   需要在认证服务侧把 --external-url 的域名加进 legal_domain 白名单，
		   回调地址为 <external-url>/api/auth/oauth/callback/<state>。

	安全提示:
		- 默认只监听 127.0.0.1。监听非回环地址时必须设置 --password 或 --auth-url。
		- 访问 token 保存在配置目录的 webui.json（权限 0600）。
		- 写操作要求浏览器 Origin 指向本服务。若通过反向代理或映射后的端口访问，
		  需用 --trusted-origin 声明对外地址，否则会返回 403 Origin 不匹配。
`,
		Category: "阿里云盘",
		Before:   nil,
		Action: func(c *cli.Context) error {
			opt := &Options{
				Host:        c.String("host"),
				Port:        c.Int("port"),
				Password:    c.String("password"),
				AllowShell:  c.Bool("allow-shell"),
				TLSCert:     c.String("tls-cert"),
				TLSKey:      c.String("tls-key"),
				AuthURL:     strings.TrimSpace(c.String("auth-url")),
				ExternalURL: strings.TrimSpace(c.String("external-url")),
			}
			for _, u := range c.StringSlice("auth-user") {
				if s := strings.TrimSpace(u); s != "" {
					opt.AuthUsers = append(opt.AuthUsers, s)
				}
			}
			for _, r := range c.StringSlice("local-root") {
				if s := strings.TrimSpace(r); s != "" {
					opt.LocalRoots = append(opt.LocalRoots, s)
				}
			}
			for _, o := range c.StringSlice("trusted-origin") {
				if s := strings.TrimSpace(o); s != "" {
					opt.TrustedOrigins = append(opt.TrustedOrigins, s)
				}
			}
			if err := RunWebUI(opt); err != nil {
				fmt.Fprintln(os.Stderr, "启动 Web 服务失败:", err)
				return err
			}
			return nil
		},
		Flags: []cli.Flag{
			cli.StringFlag{
				Name:  "host",
				Usage: "监听地址。默认只监听回环地址，设置为非回环地址时必须同时设置 password",
				Value: DefaultHost,
			},
			cli.IntFlag{
				Name:  "port",
				Usage: "监听端口",
				Value: DefaultPort,
			},
			cli.StringFlag{
				Name:  "password",
				Usage: "访问口令。不设置则自动生成随机 token 并打印到终端",
			},
			cli.StringSliceFlag{
				Name:  "local-root",
				Usage: "允许在网页里浏览的服务器本地根目录，可指定多次。默认为用户家目录和默认下载目录",
			},
			cli.BoolFlag{
				Name:  "allow-shell",
				Usage: "允许网页控制台执行 run 命令（执行任意系统命令）。仅在监听回环地址时可用",
			},
			cli.StringSliceFlag{
				Name:  "trusted-origin",
				Usage: "额外信任的浏览器来源，形如 https://pan.example.com，可指定多次。反向代理或端口映射导致 Origin 与监听地址不一致时使用",
			},
			cli.StringFlag{
				Name:  "tls-cert",
				Usage: "TLS 证书文件路径",
			},
			cli.StringFlag{
				Name:  "tls-key",
				Usage: "TLS 私钥文件路径",
			},
			cli.StringFlag{
				Name:  "auth-url",
				Usage: "第三方认证服务地址，形如 https://auth.example.com。设置后登录页会多出第三方登录入口，token/口令登录仍然可用",
			},
			cli.StringSliceFlag{
				Name:  "auth-user",
				Usage: "允许通过第三方登录的用户名，可指定多次。不指定则完全信任认证服务自身的白名单",
			},
			cli.StringFlag{
				Name:  "external-url",
				Usage: "本服务的对外访问地址，形如 https://pan.example.com，用于拼接第三方登录的回调地址。放在反向代理后面时建议显式指定（会自动并入可信来源）",
			},
		},
	}
}

// RunWebUI 启动 Web 服务，阻塞直到收到退出信号
func RunWebUI(opt *Options) error {
	if opt == nil {
		opt = &Options{}
	}
	if opt.Host == "" {
		opt.Host = DefaultHost
	}
	if opt.Port <= 0 {
		opt.Port = DefaultPort
	}

	// 安全检查：监听非回环地址必须有一种由运维显式掌控的凭据 —— 自己设的口令，
	// 或者把身份校验交给认证服务。只靠自动生成的 token 不算，那是给本机用的。
	if !isLoopbackHost(opt.Host) && opt.Password == "" && strings.TrimSpace(opt.AuthURL) == "" {
		return fmt.Errorf("监听非回环地址 %s 时必须通过 --password 设置访问口令，或通过 --auth-url 接入认证服务", opt.Host)
	}
	if opt.AllowShell && !isLoopbackHost(opt.Host) {
		return fmt.Errorf("--allow-shell 只能在监听回环地址时使用，当前监听 %s", opt.Host)
	}

	// 刷新一次配置，保证拿到最新的登录状态
	if err := config.Config.Reload(); err != nil {
		fmt.Printf("警告: 重载配置错误: %s\n", err)
	}

	srv, err := NewServer(opt)
	if err != nil {
		return err
	}
	return srv.Run()
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	}
	return false
}
