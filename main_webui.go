package main

import (
	"github.com/tickstep/aliyunpan/internal/webui"
	"github.com/urfave/cli"
)

// extraCommands 是本 fork 新增命令的统一注册点。
//
// 之所以单独放一个文件，是为了让 main.go 的改动永远只有一行
// （app.Commands = append(app.Commands, extraCommands()...)），
// 这样 `git merge upstream/main` 的冲突面能收敛到最小。
// 以后再加 fork 专属命令，往这里塞即可，不要再动 main.go。
func extraCommands() []cli.Command {
	return []cli.Command{
		// 启动 Web 管理界面
		webui.CmdWebUI(),
	}
}
