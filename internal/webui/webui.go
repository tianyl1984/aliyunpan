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

	安全提示:
		- 默认只监听 127.0.0.1。监听非回环地址时必须设置 --password。
		- 访问 token 保存在配置目录的 webui.json（权限 0600）。
		- 写操作要求浏览器 Origin 指向本服务。若通过反向代理或映射后的端口访问，
		  需用 --trusted-origin 声明对外地址，否则会返回 403 Origin 不匹配。
`,
		Category: "阿里云盘",
		Before:   nil,
		Action: func(c *cli.Context) error {
			opt := &Options{
				Host:       c.String("host"),
				Port:       c.Int("port"),
				Password:   c.String("password"),
				AllowShell: c.Bool("allow-shell"),
				TLSCert:    c.String("tls-cert"),
				TLSKey:     c.String("tls-key"),
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

	// 安全检查：监听非回环地址必须设置口令
	if !isLoopbackHost(opt.Host) && opt.Password == "" {
		return fmt.Errorf("监听非回环地址 %s 时必须通过 --password 设置访问口令", opt.Host)
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
