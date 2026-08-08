# aliyunpan Web 端改造方案（fork 友好 / 最小侵入版）

> 状态：**已实施**
> 日期：2026-08-01
> 基线：v0.4.0（commit 2feaa3c）
> 说明：本文为 fork 专属文档，上游 `tickstep/aliyunpan` 不存在此文件，合并时不会冲突。
> 实施结果与后续维护要点见文末「实施记录」。

## Context

本仓库是 `tickstep/aliyunpan` 的 fork，需要长期能 `git merge upstream/main`。因此**首要约束不是代码优雅，而是让 web 相关改动尽可能只落在上游不会碰的新文件里**。

已确认的其他约束：

| 维度 | 决定 |
|---|---|
| 部署形态 | 单机自托管、单实例，`aliyunpan webui --port 8080`，与 CLI 共用 `aliyunpan_config.json` |
| 前端 | Vue3 + Vite，`go:embed` 打进单二进制，保持 `CGO_ENABLED=0` 全平台交叉编译 |
| 一等公民 UI | 文件管理 + 传输、账号与配置。sync / share / album 走兜底控制台 |
| 命令覆盖 | 兜底控制台保证 100% |

### 关键结论

经核实，**v1 只需要修改上游文件 1 行**（`main.go` 插一句 `append`），其余全部是新增文件。原因有三个：

1. **业务逻辑其实不在本仓库**。`internal/command/*.go` 大多是「拼路径 → 调 `github.com/tickstep/aliyunpan-api` → `fmt.Println`」的薄壳。例如 `RunMkdir`（`internal/command/mkdir.go:54`）核心就一句 `MkdirByFullPath(driveId, fullpath)`。新包直接调同一个外部库即可，**无需重构 `internal/command`，也几乎没有重复代码**。
2. **`cmder.App()` 是包级单例**（`cmder/cmder_helper.go`），`main.go:108` 已 `cmder.SetApp(app)`。兜底控制台可以直接 `cmder.App().Run(argv)`，**不需要把 `main.go` 的命令组装抽成 `BuildCommands()`**。
3. **`taskframework.TaskUnit` 是接口**（`internal/taskframework/task_unit.go:19-35`），而 `pandownload.DownloadTaskUnit` 字段全部导出。新包可以写一个**委托包装器**实现 `TaskUnit`，包住原 TaskUnit 转发全部方法，从而在 `SetTaskInfo/Run/OnSuccess/OnFailed/OnComplete/OnCancel/OnRetry` 这些切面上白拿到每个文件的状态变迁 —— **零侵入拿到进度事件**。

原本计划中「抽 service 层」「给 `config.Config` 加 RWMutex」「补 `executor.Pause/Resume`」「修 `os.Exit`/`fmt.Scan` 忙循环」这些高冲突改动，在下面的设计里**全部不需要**。

---

## 一、文件布局：新增为主

```
main_webui.go                          [新] package main，唯一的注册入口
main.go                                [改 1 行] app.Commands = append(app.Commands, extraCommands()...)
web/                                   [新] Vue3 + Vite 源码
build_web.sh                           [新] 前端构建脚本（不动 build.sh）
internal/webui/                        [新] 全部后端代码，上游永不触碰
├── cmd.go            CmdWebUI() cli.Command + RunWebUI(opt)
├── server.go router.go middleware.go resp.go
├── svc_file.go       文件操作：直接调 aliyunpan-api
├── svc_account.go    账号/配额/网盘/扫码登录
├── svc_config.go     配置读写（字段白名单）
├── svc_localfs.go    服务器本地目录浏览（根白名单）
├── transfer.go       Manager / Job / 状态机
├── transfer_unit.go  ★ 委托包装器：实现 taskframework.TaskUnit，包住 pandownload/panupload
├── transfer_probe.go ★ 字节级进度探针（stat 分片临时文件）
├── console.go        兜底控制台：cmder.App().Run(argv) + stdout 重定向
├── events.go         SSE EventBroker
├── static.go         //go:embed all:assets/dist
└── assets/dist/      [新] Vite 产物，提交进 git
```

**合并策略**：所有 web 工作只碰 `internal/webui/`、`web/`、`main_webui.go`、`build_web.sh`、`assets/dist`。`git merge upstream/main` 的冲突面永久收敛到 `main.go` 那 1 行。建议在 fork 里维护一个 `webui` 长期分支，定期 rebase 上游。

### `main.go` 的唯一改动

在 `main.go:661`（`sort.Sort(cli.FlagsByName(...))` 之前）插入一行：

```go
app.Commands = append(app.Commands, extraCommands()...)
```

`main_webui.go`（新文件）：
```go
package main

import (
    "github.com/tickstep/aliyunpan/internal/webui"
    "github.com/urfave/cli"
)

// extraCommands fork 新增命令的统一注册点，避免污染 main.go
func extraCommands() []cli.Command {
    return []cli.Command{ webui.CmdWebUI() }
}
```

以后再加任何 fork 专属命令都往这个文件里塞，`main.go` 的 diff 永远是那 1 行。

**不引入任何新 Go 依赖**（纯标准库 `net/http` + Go 1.22 `ServeMux` 的 method 路由 + SSE），`go.mod`/`go.sum` 零改动 —— 这两个文件是上游合并的高频冲突点。

---

## 二、文件与账号操作：新包直连 API

不做 service 层抽取，`internal/webui/svc_*.go` 直接用现有的全局入口：

```go
// internal/webui/svc_file.go
func activeUser() (*config.PanUser, error) {   // 自己做 nil 检查，绕开 command.GetActiveUser() 的 panic
    u := config.Config.ActiveUser()
    if u == nil || u.UserId == "" { return nil, ErrNotLogin }
    return u, nil
}

func List(driveId, absPath string, orderBy, orderDir) (*aliyunpan.FileEntity, aliyunpan.FileList, error)
func Mkdir(driveId, absPath string) (*aliyunpan.MkdirResult, error)
func Remove(driveId string, absPaths []string) ([]ItemResult, error)
func Copy/Move/Rename/Search(...)
```

复制成本极低的部分（都是 5-10 行、且是对外部库的薄封装）：
- `matchPathByShellPattern` → 直接调 `OpenapiPanClient().MatchPathByShellPattern`
- `GetAllPathFolderByPath` + `activeUser.DeleteCache(...)` 的缓存清理
- 插件回调 `plugins.NewPluginManager(config.GetPluginDir())`（`plugins` 包已导出，v1 可先不接）

**路径一律绝对**：Web 端不读写 `PanUser.Workdir`/`ResourceWorkdir`/`AlbumWorkdir`，前端 Pinia 持有 `currentPath`，刷新靠 URL query 恢复。好处是多标签页互不干扰、不污染 CLI 的 `pwd`、不因浏览目录触发配置落盘。

**账号**：`config.Config.SetActiveUser/SwitchUser/DeleteUser/NumLogins` 全部已导出，直接调。扫码登录复用 `panlogin.NewLoginHelper(config.DefaultTokenServiceWebHost)`：`GetQRCodeLoginUrl` 拿 ticketId → 前端开新窗口做授权 → 后端轮询 `GetLoginToken(ticketId)` 替代 CLI 里的「按 Enter」。这条路径本来就是浏览器流程，Web 化比 CLI 更自然。

---

## 三、传输：委托包装器 + 磁盘探针（零侵入的核心技巧）

### 3.1 Job 组装

`internal/webui/transfer.go` 复制 `RunDownload`（`internal/command/download.go:227-470`）里约 60 行的**参数装配**逻辑：`downloader.Config` 构造、并发上限钳制（`MaxFileDownloadParallelNum=3` / `MaxFileUploadParallelNum=20`）、`SaveTo` 解析、通配符展开。`pandownload.DownloadTaskUnit` 全部字段导出，可从外部包直接构造；`taskframework.TaskExecutor` 同样可用。上传侧对应 `panupload.UploadTaskUnit`。

这是全方案唯一实质性的逻辑复制。代价是上游若调整下载参数策略，需要手动同步 —— 但比重构 `RunDownload` 的合并冲突小得多。

### 3.2 ★ 委托包装器：白拿每文件状态

`taskframework.TaskUnit` 是接口（8 个方法），包一层即可拦截全部生命周期：

```go
// internal/webui/transfer_unit.go
type wrappedUnit struct {
    inner  taskframework.TaskUnit   // *pandownload.DownloadTaskUnit 或 *panupload.UploadTaskUnit
    job    *Job
    id     string
    path   string
    size   int64
}

func (w *wrappedUnit) SetTaskInfo(i *taskframework.TaskInfo) {
    w.id = i.Id(); w.job.sink.Register(w.id, w.path, w.size); w.inner.SetTaskInfo(i)
}
func (w *wrappedUnit) Run() *taskframework.TaskUnitRunResult {
    w.job.gate.WaitIfPaused()                                   // ← 队列级暂停，见 3.4
    if w.job.gate.IsCanceled() {
        return &taskframework.TaskUnitRunResult{Cancel: true}   // ← 未开始的文件直接取消
    }
    w.job.sink.MarkState(w.id, ui.TaskRunning, "")
    stop := w.job.probe.Watch(w.id, w.savePath, w.size)         // ← 字节级探针，见 3.3
    defer stop()
    return w.inner.Run()
}
func (w *wrappedUnit) OnSuccess/OnFailed/OnRetry/OnCancel/OnComplete(r *...) {
    w.job.sink.MarkState(w.id, mapState(r), r.ResultMessage)
    w.inner.OnXxx(r)
}
func (w *wrappedUnit) RetryWait() time.Duration { return w.inner.RetryWait() }
```

`executor.Append(&wrappedUnit{...}, maxRetry)`。**对 `internal/functions/*` 和 `internal/taskframework/*` 零改动**。

### 3.3 ★ 字节级进度：磁盘探针

下载器写的是 `SavePath + ".aliyunpan-downloading"`（`pandownload.DownloadSuffix`）。探针每 500ms `os.Stat` 这个临时文件，与已知的 `f.FileSize` 相除即得精确进度，差分即得速率。零侵入、精度足够。

全局速率另有现成来源：`GlobalSpeedsStat *speeds.Speeds` 是从外部传进 TaskUnit 的，我们自己创建、自己读。`pandownload.DownloadStatistic` 同理给出累计字节与耗时。

**上传侧**没有本地临时文件，v1 只提供「每文件状态 + 全局速率 + 已完成计数」，单文件字节进度留空（UI 显示为不确定进度条）。要补精确进度需要动 `UI` 字段，见 §六。

### 3.4 暂停 / 取消：文件边界生效

`taskframework.TaskExecutor.Pause/Resume/Stop` 是空函数（已核实 `executor.go:178-190`），但**不需要去补**：包装器的 `Run()` 在委托前先 `WaitIfPaused()` / 查 `IsCanceled()`，等价于队列级 gate。

```go
type gate struct{ mu sync.Mutex; paused bool; canceled bool; cond *sync.Cond }
```

代价说清楚：
- **暂停**：正在传输的文件会跑完，之后队列停住。暂停时最多有 `parallel` 个 goroutine 阻塞在 `WaitIfPaused()`（占着 worker 槽位，无 CPU 消耗）。
- **取消**：同理，当前文件跑完才停。
- **断点续传天然可用**：下载 `InstanceState`（`.aliyunpan-downloading`）、上传 `<configDir>` 下的 bolt 库 + `rapidUpload()` 秒传都已存在。取消后重新 Submit 同参数即自动续传。

要做到「立即中断当前文件」需要拿到内部的 `downloader.Downloader` 实例（其 `Pause/Resume/Cancel` 在 `internal/file/downloader/downloader.go:642-676` 是真实实现，但实例是 `download_task_unit.go:199` 的局部变量）。这需要动上游文件，列为 §六 的可选项。

### 3.5 Manager

```go
// internal/webui/transfer.go
type JobState string  // queued|running|paused|completed|failed|canceled
func (m *Manager) Submit(spec *JobSpec) (jobId string, err error)   // go func(){ exec.Execute(); m.finalize() }()
func (m *Manager) List/Get/Pause/Resume/Cancel/Retry/Remove(...)
func (m *Manager) Shutdown(ctx) error
```
`Execute()` 阻塞无所谓，包在 goroutine 里即可。终态 Job 存内存 LRU（默认 200 条）。

---

## 四、兜底控制台：复用 `cmder.App()`

```go
// internal/webui/console.go
var consoleMu sync.Mutex     // 进程级 os.Stdout 替换，必须全局串行

func Exec(ctx context.Context, argv []string, out io.Writer) (exitCode int, err error) {
    consoleMu.Lock(); defer consoleMu.Unlock()
    app := cmder.App()                       // ← 直接复用主 app，main.go 零改动
    oldW, oldEW := app.Writer, app.ErrWriter
    r, w, _ := os.Pipe()
    oldOut, oldErr, oldIn := os.Stdout, os.Stderr, os.Stdin
    os.Stdout, os.Stderr = w, w
    os.Stdin = nullStdin()                   // 已关闭写端的 pipe → 所有读立即 EOF
    app.Writer, app.ErrWriter = w, w
    defer restore()
    go io.Copy(out, r)                       // 边跑边推 SSE console.output
    return 0, app.Run(append([]string{"aliyunpan"}, argv...))
}
```

### denylist 让所有「必修 bug」变成不必修

| 命令 | 拒绝理由 | 替代 |
|---|---|---|
| `who` | `internal/command/user_info.go:128` 有 `os.Exit(1)`，会杀掉整个服务 | 账号页 |
| `sync` | `internal/command/sync.go:406-411` 的 `for { fmt.Scan(&c) }` 在 stdin EOF 下忙循环，CPU 打满 | 后续阶段做图形界面 |
| `login` `su` `drive` | 需要 tty（liner）或交互输序号 | 账号页已有图形化 |
| `run` | 执行任意系统命令 | 默认 deny，`--allow-shell` 显式开启 |
| `quit` `clear` `history` `env` `update` | 对 server 无意义或需交互 | — |

`rename`（批量）、`logout` 走命令时**强制注入 `-y`**，避免 `fmt.Scanln` 阻塞（stdin 已 EOF，虽不阻塞但会走到确认失败分支）。

这样一来，原本方案里「阶段 0 必修 10 个 bug」缩减为 **0 个必修**。这些仍然是真实缺陷，建议向上游提 issue/PR，而不是在 fork 里本地打补丁（本地补丁 = 未来每次合并的冲突源）。

### 其余风险

- **webui 自身日志不能写 `os.Stdout`**（否则被塞进控制台输出）→ 启动后日志写 `config.GetLogDir()`，或写到启动时保存的 `oldOut` 副本。
- **超时**：`context.WithTimeout` 默认 300s。诚实说明：Go 无法强杀 goroutine，超时只能断开响应并标记 busy，真卡死需重启服务。**denylist 才是主防线**。
- **panic 兜底**：handler goroutine `defer recover()`，栈信息作为 `console.exit` 事件回前端。
- **`cmder.App().Action` 是 REPL**：必须拒绝空 argv，否则会进交互 shell 挂死。
- **`Before: ReloadConfigFunc`**：控制台里每条命令都会 `config.Config.Reload()`，会冲掉 Web 端刚改的内存态。因此 **Web 端所有写操作必须立即 `config.Config.Save()`**，不做延迟落盘（见 §五）。
- 命令元数据从 `cmder.App().Commands` 反射生成，供前端补全与置灰：`GET /api/console/commands`。
- 前端传**数组** `argv` 而非字符串，避免服务端做 shell 分词；自由输入框可复用 `cmder/cmdliner/args` 的分词器（与 CLI REPL 同一套）。

---

## 五、并发与状态：不动 `config.Config`

不给 `PanConfig` 加 RWMutex（那会散布到十几个方法，是上游高频编辑区）。改为：

1. **Web 层自持一把 `sync.Mutex`**，包住所有会写 `config.Config` 的操作（切换账号/网盘、改配置、登录登出）。解决 web-vs-web 并发。
2. **写后立即 `SaveConfigFunc`**，与 CLI 行为一致，也避免控制台的 `Reload()` 冲掉内存态。
3. **接受两个既有竞态**（不在 fork 里修）：
   - `AutomaticallyRefreshTokenTask`（`internal/command/utils.go:309`）每分钟 `Reload()+Save()`，与 HTTP handler 并发读写无锁字段。这是上游既有问题，`sync start` 常驻模式暴露面相同。
   - `Reload()`（`pan_config.go:193`）重建 `UserList` 却不清 `c.activeUser` 缓存指针（`:433` 处会短路返回旧指针），可能导致刷新到的 token 写进游离对象而丢失。

   → 两条都建议**向上游提 issue**。如果实际运行中被咬，`activeUser` 那条是 1 行本地补丁（`init()` 结尾 `c.activeUser = nil`），冲突风险可接受。
4. `Save()` 非原子（`Truncate+Seek+Write`，`:156-190`）同样是既有问题，不在 fork 里改。

---

## 六、可选增强（需要动上游文件，按需开启）

以下三项**不在 v1 范围**，仅在实际需要时才做，并明确列出会污染哪些上游文件：

| 增强 | 改动 | 涉及文件 |
|---|---|---|
| 上传/下载**单文件字节级进度**（下载已由探针解决，主要收益在上传） | 新增 `internal/ui/sink.go` 定义 `ProgressSink` 接口（`*DashboardPanel` 已核实零改动满足：`dashboard_panel.go:172/192/209/225/243` 五个方法签名逐字一致）；两处字段类型 `UI *ui.DashboardPanel` → `UI ui.ProgressSink` | `pandownload/download_task_unit.go:79`、`panupload/upload_task_unit.go:85` |
| ↑ 附带必须处理的 typed-nil 陷阱 | `download.go:451` / `upload.go:412` 写的是 `UI: dashboard`，`dashboard` 声明为 `*ui.DashboardPanel = nil`。转接口后 `dtu.UI != nil` **恒为真**。`DashboardPanel` 方法都有 nil-receiver 保护所以不会 panic，但 `logf` 会走进面板分支并被静默丢弃 → **不带 `-ui` 时下载日志全部消失**。需改 `logf` 判空 + 赋值处用 `ui.OrNop(dashboard)` | +4 行，共 4 个上游文件 |
| **立即中断**正在传输的文件 | 给两个 TaskUnit 加 `Control` 字段，在 `download_task_unit.go:199` 拿到 `der` 后 `Bind`/`defer Unbind`，`Run()` 开头查 `IsCanceled()` | 约 4 行，2 个上游文件 |

判断标准：v1 先上，如果「暂停要等当前大文件跑完」在实际使用中不可忍受，再做第三项。

---

## 七、浏览器上传 / 下载

**上传模式 A（默认）**：前端弹「服务器文件浏览器」（`GET /api/local/ls`），选中后 `POST /api/transfer/upload`。100% 复用 `panupload` 管线 —— 秒传、分片、断点、bolt 记录全部生效。自托管场景文件通常本就在这台机器上。

**上传模式 B（浏览器直传）**：必须落盘暂存，不能流式直穿 —— `rapidUpload()` 秒传要整文件 SHA1 + proof code，分片上传要可重复读的 `io.ReaderAt`。
`POST /api/upload/session` → `PUT /{id}/chunk?index=N`（前端 `File.slice()`，服务端 `WriteAt`，支持乱序重传）→ `POST /{id}/complete` 转为普通上传 Job。暂存在 `<configDir>/webui_stage/<uuid>/`，完成/取消/超时（24h）清理。代价是等大磁盘占用，UI 明示。

**下载模式 A（默认）**：下到服务器磁盘，复用 `pandownload` 全部能力。

**下载模式 B（流式回传）**：`GET /api/files/content` —— **服务端代理转发**，不做 302。阿里 CDN 直链对 Referer/UA 有要求且有效期极短，直接交给浏览器容易 403；代理还能隐藏 token。透传 `Range` 头，原样回写 `Content-Length`/`Content-Range`/`Accept-Ranges`，`Content-Disposition: attachment; filename*=UTF-8''<encoded>`。另有 `/api/files/preview`（inline + MIME 白名单）与 `/api/files/thumbnail`。

---

## 八、SSE 与 API

### 为什么 SSE 不是 WebSocket
进度是纯单向推送；仓库当前零 WebSocket 依赖，SSE 用标准库 `net/http` + `http.Flusher` 即可，**不新增 go.mod 条目**（合并友好）；`EventSource` 自带断线重连。用**单条多路复用流** `/api/events` 绕开浏览器每域 6 连接限制。代价：`EventSource` 不能带自定义 header → 认证必须走 Cookie（本方案本就如此）。

**节流是硬要求**：`task.progress` 每 taskId 每 500ms 最多一条；Broker 用有界 channel + 丢弃最旧，慢消费者绝不能阻塞探针/传输协程。

事件类型：`job.added` / `job.state` / `job.progress` / `task.progress` / `task.state` / `log` / `console.output` / `console.exit` / `account.changed` / `ping`(25s)。前端首连先拉 `GET /api/transfer/jobs` 全量快照，再靠 SSE 增量；重连后重拉（幂等）。

### 端点清单

**账号与配置**
```
GET  /api/auth/status          POST /api/auth/login        POST /api/auth/logout
GET  /api/account/current      /api/account/list  /api/account/quota  /api/account/drives
POST /api/account/switch       /api/account/drive/switch
DELETE /api/account/{userId}
POST /api/account/oauth/start          → {ticketId, authorizeUrl, expiresIn}
GET  /api/account/oauth/poll?ticketId= → 轮询 GetLoginToken，替代「按 Enter」
GET/PUT /api/config            字段白名单，token 脱敏
GET  /api/system/info          版本、配置路径、监听地址、console/shell 开关
```

**文件管理**
```
GET  /api/files?driveId=&path=&orderBy=&order=&marker=&limit=
GET  /api/files/info    /api/files/search
POST /api/files/{mkdir|delete|copy|move|rename}
GET  /api/files/{content|preview|thumbnail}
GET  /api/local/roots   /api/local/ls?path=
```

**传输**
```
GET    /api/transfer/jobs?state=&type=       /api/transfer/jobs/{id}
POST   /api/transfer/download                /api/transfer/upload
POST   /api/transfer/jobs/{id}/{pause|resume|cancel|retry}      /api/transfer/clear
DELETE /api/transfer/jobs/{id}
POST/PUT/POST/DELETE  /api/upload/session[/{id}/chunk?index=|/{id}/complete|/{id}]
```

**事件与控制台**
```
GET  /api/events              SSE
GET  /api/console/commands    从 cmder.App().Commands 反射生成
POST /api/console/exec        {argv:[...], timeoutSec?} → 202 {sessionId}
```

统一响应 `{"code":0,"message":"ok","data":{...}}`。

---

## 九、前端与构建

```
web/
├── vite.config.ts    build.outDir = '../internal/webui/assets/dist'
│                     server.proxy '/api' → http://127.0.0.1:8080
└── src/
    ├── api/{http,files,transfer,account,console,events}.ts
    ├── stores/{auth,files,transfer,config}.ts        # Pinia
    ├── components/{FileTable,Breadcrumb,LocalPathPicker,TransferItem,UploadDropzone}
    └── views/  LoginView(/login) FilesView(/files) TransferView(/transfer)
                AccountView(/account) SettingsView(/settings) ConsoleView(/console)
```
组件库 Element Plus 或 Naive UI。

**go:embed**：`//go:embed all:assets/dist` 只能嵌入所在包目录及子目录 → Vite `outDir` 必须指到 `internal/webui/assets/dist`。SPA fallback：非 `/api` 前缀且文件不存在 → 返回 `index.html`。`/assets/*` 打 `Cache-Control: max-age=31536000,immutable`（Vite 文件名带 hash），`index.html` 打 `no-cache`。

**目录不存在时 `//go:embed` 编译失败**，且项目无 CI、`build.sh` 直接 `go build` → **必须把 `assets/dist` 提交进 git**（加 `.gitattributes` 标 `linguist-generated` 让 diff 折叠）。这样 `go build ./...` 和 `go install ...@latest` 都开箱可用。

**不改 `build.sh`**（它是上游文件）。新增 `build_web.sh`：
```sh
#!/bin/sh
cd web && npm ci && npm run build     # 产物与 GOOS/GOARCH 无关，跨平台编译前跑一次即可
```
`CGO_ENABLED=0` 不受影响 —— `embed` 是纯 Go。`.gitignore` 追加 `web/node_modules`（**不要**忽略 `internal/webui/assets/dist`）。

---

## 十、安全

| 项 | 措施 |
|---|---|
| 监听地址 | 默认 `--host 127.0.0.1`。非回环时强制要求已设密码，否则**拒绝启动**并打印说明 |
| 认证 | 两种方式并存。① token/口令：`--password` 或首次自动生成随机 token 打印到终端（存 `<configDir>/webui.json`，0600）。② 第三方登录：`--auth-url` 接入外部认证服务，见下节。会话 Cookie `HttpOnly; SameSite=Strict; Secure(TLS 时)`，存内存，默认 7 天。密码只存 bcrypt/scrypt 哈希 |
| 暴力破解 | 登录接口按 IP 限速（失败 5 次锁 5 分钟） |
| CSRF | 三重：`SameSite=Strict` + 写操作校验 `Origin`/`Referer` + 要求自定义头 `X-Aliyunpan-Client`（简单请求带不了 → 触发预检） |
| SSE 认证 | 走 Cookie（`EventSource` 唯一选择）。**不要**用 URL query 传 token（会进日志） |
| 路径穿越 `/api/local/*` | 根白名单（默认家目录 + 默认下载目录，`--local-root` 追加）。校验链 `Clean` → `Abs` → **`EvalSymlinks`**（防 symlink 逃逸）→ `HasPrefix(resolved, root+sep)`；Windows 用 `EqualFold` 比盘符 |
| 上传暂存 | 固定 `<configDir>/webui_stage/`，文件名服务端生成 UUID，**永不用客户端文件名拼路径** |
| 下载 SavePath | `filepath.Join(saveRoot, 清洗后相对路径)`，过滤 `..` 与绝对路径前缀 |
| `run` / `tool enc,dec` | 默认 deny；`run` 需 `--allow-shell` 且监听回环 |
| 敏感数据 | `/api/config` 与 `/api/account/*` **绝不返回** `openapiToken`/`webapiToken`/`ClientSecret`；写接口字段白名单，禁止反序列化整个 `PanConfig` |
| TLS | 可选 `--tls-cert/--tls-key`；非回环时提示置于反代之后 |

### 十之二、第三方登录（`internal/webui/auth_oauth.go`）

把身份校验委托给外部认证服务 [cf-worker-auth](https://github.com/tianyl1984/cf-worker-auth)（Cloudflare Worker + GitHub OAuth）。
**token/口令登录始终保留**，第三方登录是并列的第二个入口，只有配了 `--auth-url` 才出现。
webui 侧不接触 GitHub OAuth，不需要 client secret，也不引入任何新的 Go 依赖。

```
浏览器                         aliyunpan webui                认证服务
  │ 1. 点「使用 GitHub 登录」        │                            │
  │ ─ GET /api/auth/oauth/start ──▶│ 生成 state，种 Lax Cookie   │
  │ ◀────── 302 ───────────────────│                            │
  │ 2. GET <auth>/login?callback=<external>/api/auth/oauth/callback/<state> ─▶│
  │ 3. GitHub 授权 + 认证服务校验 legal_user 白名单               │
  │ ◀────── 302 到 callback?token=xxx ────────────────────────────│
  │ 4. GET /api/auth/oauth/callback/<state>?token=xxx ─▶│         │
  │                                 │ 5. GET <auth>/userinfo ───▶│
  │                                 │    (token 走 Bearer 头)    │
  │                                 │ 6. 校验 --auth-user 白名单  │
  │ ◀── 302 到站内页面 + 会话 Cookie ─│                            │
```

| 关键点 | 说明 |
|---|---|
| state 走路径而非 query | 认证服务回调时固定拼 `?token=xxx`，回调地址自身**不能再带 query**，否则参数会被拼坏。故回调地址形如 `.../callback/<state>` |
| state Cookie 是 `SameSite=Lax` | 认证服务是跨站 302 跳回来的，`Strict` 的 Cookie 在跨站顶级导航里不会被带上。会话 Cookie 仍然是 `Strict` |
| state 一次性 | 存服务端内存，`consume` 即删，过期 10 分钟，同时在场上限 512 个 |
| 回调地址推导 | 优先 `--external-url`；没配就用请求 `Host`，但必须通过 `originAllowed`（指向监听地址或已被 `--trusted-origin` 声明），否则伪造 Host 头就能把回调指到别处。反代终止 TLS 时额外试一次 `https` |
| `--external-url` 自动并入可信来源 | 免得用户还要再写一遍 `--trusted-origin` |
| 二次白名单 | `--auth-user` 可再限一层；不配则完全信任认证服务自身的 `legal_user` |
| 登录后跳转 | `redirect` 只接受站内绝对路径（拒 `//`、`/\`、绝对 URL、CRLF、超长），避免变成开放重定向 |
| 出错处理 | 回调是浏览器整页导航，不能返回 JSON，一律 302 回 `/login?error=<原因>` 由前端弹 toast |
| 部署前提 | `--external-url` 的域名要加进认证服务 KV 的 `legal_domain`，登录用户要在 `legal_user` 里 |

启动示例：

```sh
aliyunpan webui --host 0.0.0.0 \
  --external-url https://pan.example.com \
  --auth-url https://auth.example.com \
  --auth-user your-github-id
```

监听非回环地址的强制凭据检查也随之放宽：`--password` **或** `--auth-url` 满足其一即可。

Docker 部署下这三个参数由 `docker/webui/entrypoint.sh` 从环境变量翻译过来
（`webui` 命令本身只认命令行参数，不读环境变量）：

| 环境变量 | 对应参数 |
|---|---|
| `ALIYUNPAN_WEBUI_EXTERNAL_URL` | `--external-url` |
| `ALIYUNPAN_WEBUI_AUTH_URL` | `--auth-url` |
| `ALIYUNPAN_WEBUI_AUTH_USERS` | `--auth-user`（逗号分隔，逐个展开） |

entrypoint 的口令强制检查同步放宽为「`ALIYUNPAN_WEBUI_PASSWORD` 或 `ALIYUNPAN_WEBUI_AUTH_URL` 二选一」，
并在启用第三方登录却没设 `EXTERNAL_URL` 时告警 —— 容器场景下回调地址几乎不可能靠请求 Host 推导正确。

---

## 十一、分阶段路线

### 阶段 1 — 只读 Web 跑起来
产出：`main_webui.go` + `main.go` 那 1 行；`internal/webui/` 的 server/router/middleware/static/svc_file(List,Info,Search)/svc_account(只读)；`web/` 骨架 + LoginView + FilesView（只读）+ AccountView（只读）+ `build_web.sh`。

**验收**：`aliyunpan webui` 启动 → 浏览器输 token → 浏览备份盘/资源库；同时终端 `aliyunpan ls` 正常；`--host 0.0.0.0` 无密码时拒绝启动；`git diff upstream/main -- main.go` 只有 1 行。

### 阶段 2 — 文件写操作 + 账号管理
产出：`svc_file` 补齐 Mkdir/Remove/Copy/Move/Rename；`svc_config` 读写；扫码登录（oauth start + poll）、账号切换/登出/配额/网盘切换；前端右键菜单、多选、删除确认、SettingsView。

**验收**：Web 完成「新建→重命名→移动→复制→删除」全链路；扫码登录新账号后 `aliyunpan loglist` 能看到；Web 改配置后 CLI `aliyunpan config` 显示一致。**仍然零上游文件改动**。

### 阶段 3 — 传输（核心）
产出：`transfer.go`（Manager + gate）、`transfer_unit.go`（委托包装器）、`transfer_probe.go`（磁盘探针）、`events.go`（SSE + 节流）；前端 TransferView + LocalPathPicker + 流式下载/预览。

**验收**：并发下 3 个大文件，Web 实时显示每文件字节进度与全局速率；暂停 → 当前文件跑完后队列停住 → 继续 → 从断点续传（校验 `.aliyunpan-downloading` 被复用）；取消后重新提交走秒传/续传；kill 服务重启后重新提交能续传；`aliyunpan download` 的 CLI 面板行为与改动前完全一致（因为根本没改它）。**仍然零上游文件改动**。

### 阶段 4 — 兜底控制台 + 浏览器直传
产出：`console.go`（复用 `cmder.App()` + stdout 重定向 + 全局互斥 + denylist + stdin EOF + 超时）；`/api/console/commands`；前端 ConsoleView（补全/历史/实时输出）；分片直传 + 拖拽上传。

**验收**：控制台能正确回显 `tree` / `share list` / `album list` / `ls`；`who` / `sync` / `login` 被 denylist 拒绝并给出替代提示；跑读 stdin 的命令不挂死服务；100MB 拖拽上传成功且暂存被清理。**仍然零上游文件改动**。

### 阶段 5（可选，按需）
§六 的三项增强（上传字节进度、立即中断）；任务列表持久化 `<configDir>/webui_tasks.json`；Docker 镜像（参考 `docker/sync/`）；sync / share / album 图形界面。**只有这一阶段才会碰上游文件，且每项都可独立取舍。**

---

## 十二、上游合并维护

- 保持 `git remote add upstream https://github.com/tickstep/aliyunpan.git`，定期 `git fetch upstream && git merge upstream/main`。阶段 1-4 的预期冲突：**仅 `main.go` 那 1 行**，且是 append 语句，多数情况自动合并。
- `internal/webui/transfer.go` 里复制的参数装配逻辑是唯一需要人工跟进的地方 —— 上游若调整下载/上传的并发策略或 `downloader.Config` 字段，需同步。建议在该文件顶部注释标注「同步自 internal/command/download.go:262-405 @ v0.4.0」，便于日后 diff。
- 发现的上游 bug（`user_info.go:128` 的 `os.Exit`、`sync.go:406` 的 `fmt.Scan` 忙循环、`pan_config.go:193` 的 activeUser 缓存分裂、`Save()` 非原子、`executor.go:178-190` 的空实现、`album_web.go` 787 行死代码）**向上游提 issue/PR，不在 fork 里本地打补丁** —— 本地补丁是未来每次合并的冲突源，而 denylist 已让它们对 Web 端无害。

---

## 实施记录（2026-08-01）

方案已完整落地。以下是与设计稿的差异、验证结果和维护要点。

### 对上游文件的实际改动

| 文件 | 改动 |
|---|---|
| `main.go` | **1 行**：`app.Commands = append(app.Commands, extraCommands()...)` |
| `.gitignore` | 追加 `web/node_modules/`（新增段落，不改已有行） |

其余全部为新增文件：`main_webui.go`、`build_web.sh`、`.gitattributes`、`internal/webui/**`、`web/**`。
**`go.mod` / `go.sum` 零改动** —— 后端只用标准库 `net/http` + SSE，没有引入任何新的 Go 依赖。

### 与设计稿的差异

1. **前端不使用组件库**。设计稿建议 Element Plus / Naive UI，实际手写 CSS。原因：产物要提交进 git，组件库会让 dist 从 ~150KB 涨到 1MB+。当前产物 154KB（gzip 60KB），依赖只有 vue / vue-router / pinia。
2. **搜索用递归列目录实现**。阿里云盘 OpenAPI（`aliyunpan-api v0.2.9`）没有提供搜索接口，`internal/command` 里也没有 search 命令。`/api/files/search` 用递归 `FileListGetAll` 实现，限定深度 4、结果 200 条，并支持请求取消。
3. **传输任务的 stdout 静音**用了一个技巧：给 `DownloadTaskUnit.UI` 传一个 `Output: io.Discard` 且从不 `Start()` 的 `ui.DashboardPanel`。任务单元在 `UI != nil` 时完全不写 `os.Stdout`（见 `download_task_unit.go` 的 `OnDownloadStatusEvent` 与 `logf`），面板不 Start 就不渲染、不起协程。见 `transfer_download.go:silentPanel`。
4. **控制台超时诚实化**。设计稿说超时「只能断开响应」。实际实现：`app.Run` 跑在独立协程里，超时后 `Exec` 立即返回错误告知用户，后台协程等命令真正结束后再恢复 `os.Stdout` 并释放互斥锁。期间控制台不可用，前端会看到明确提示。
5. **取消不立即置终态**。`Job.Cancel()` 只设 `正在取消…` 消息，最终状态由执行器退出后的 `runJob` 写入。否则 `Shutdown`/`Remove` 会误判任务已结束。
6. **浏览器直传的任务不支持重试**：暂存文件在任务结束时已删除，`Manager.Retry` 会明确拒绝并提示重新选择文件。

### 验证结果

- `go build ./...`、`go vet ./internal/webui/` 通过，`gofmt` 干净。
- **CLI 回归**：与改动前的二进制逐条对比 `config` / `lls` / `lpwd` / `quota` / `who` / `loglist` / `ls` / `env`，输出**逐字节一致**；`help` 仅多出一行 `webui`。
- **接口实测**（未登录云盘状态下可覆盖的部分）：认证、会话 Cookie、限速锁定、CSRF（缺自定义头 / 错误 Origin / 缺 Origin 三种都被拒）、未知字段拒绝、路径穿越防护（`/etc` 与 `/Users/x/../../etc` 均 403）、SPA 回落与静态资源缓存头、SSE 实时推送、控制台执行与 denylist。
- **控制台**实测能完整捕获 `cmdtable` 直写 `os.Stdout` 的表格输出并实时推送。
- `go test ./...`：`panlogin` 与 `syncdrive` 各有 1 个失败，已确认在**改动前的原始代码上同样失败**（前者硬编码连 `localhost:8977` 后 nil 解引用，后者硬编码 Windows 路径），与本次改动无关。
- **未实测**：需要真实阿里云盘账号才能跑的路径 —— 扫码登录、文件增删改、下载/上传任务、断点续传、浏览器直传。这些需要用真实账号手工验收。

### 维护要点

1. **唯一需要跟进上游的地方**是 `internal/webui/transfer_download.go` 与 `transfer_upload.go` 里复制的参数装配逻辑（同步自 `internal/command/download.go` 的 `RunDownload` 和 `upload.go` 的 `RunUpload` @ v0.4.0）。上游若调整并发策略或 `downloader.Config` / `UploadTaskUnit` 的字段，需要手工同步。两个文件顶部都有注释标注来源。
2. **前端改动后必须重新构建并提交 dist**：`./build_web.sh` → `go build`。`//go:embed all:assets/dist` 在目录为空时会编译失败。
3. **认证凭据**：不带 `--password` 启动时，token 存在 `<configDir>/webui.json`（0600，明文）。这与配置文件里本来就明文存放 OAuth token 的做法一致；带 `--password` 时只存派生哈希，不存明文。
4. **第三方登录只碰 fork 自己的文件**：新增 `internal/webui/auth_oauth.go`，改动 `auth.go`（会话结构加 `user` 字段）、`router.go`、`server.go`、`middleware.go`、`webui.go` 与前端三个文件。上游文件仍然零改动，`go.mod` 仍然零改动。
5. **已知的权限边界**：网页控制台等价于完整的 CLI 权限（可以执行 `download -saveto <任意路径>`），因此 `--local-root` 白名单只约束图形界面的文件浏览与传输接口，不约束控制台。这是设计使然 —— 控制台的定位就是「兜底的完整 CLI」，它的防线是访问认证本身。

---

## 实施记录（2026-08-08）：第三方登录

在原有 token/口令登录之外新增第二种登录方式，接入 cf-worker-auth。设计与安全要点见 §十之二。

### 改动清单

| 文件 | 改动 |
|---|---|
| `internal/webui/auth_oauth.go` | **新增**。`oauthManager`（state 生命周期、认证服务客户端、白名单）+ start/callback 两个处理器 + 回调地址推导 |
| `internal/webui/auth.go` | 会话由 `map[string]time.Time` 改为 `map[string]*session`，多带一个 `user` 字段；抽出 `newSession`（不校验凭据建会话）与 `lookup` |
| `internal/webui/router.go` | 注册 `GET /api/auth/oauth/start` 与 `GET /api/auth/oauth/callback/{state}` |
| `internal/webui/middleware.go` | `/api/auth/status` 增加 `user` 与 `oauth` 字段，供登录页决定是否显示按钮 |
| `internal/webui/server.go` | 构造 `oauthManager`；`--external-url` 并入可信来源；banner 打印认证服务与回调地址 |
| `internal/webui/webui.go` | 新增 `--auth-url` / `--auth-user` / `--external-url` 三个 flag；非回环地址的凭据检查放宽为「口令或认证服务二选一」 |
| `web/src/stores/index.js` | auth store 增加 `user` / `oauth` / `loginExternal` |
| `web/src/views/LoginView.vue` | 分隔线 + 第三方登录按钮（仅服务端启用时显示）；读 `?error=` 弹 toast 并清掉 query |
| `web/src/App.vue` | 侧栏显示当前 Web 登录者 |
| `docker/webui/entrypoint.sh` | 三个新环境变量翻译成参数；口令检查放宽为「口令或认证服务二选一」；缺 `EXTERNAL_URL` 时告警 |
| `docker/webui/docker-compose.yml` | 透传三个新变量；`ALIYUNPAN_WEBUI_PASSWORD` 由 `:?`（缺失即报错）改为可空，交给 entrypoint 统一判断 |
| `docker/webui/.env.example` | 补齐三个新变量与认证服务侧的 `legal_domain` / `legal_user` 前置条件 |

### 验证结果

`go build ./...`、`go vet`、`gofmt` 干净；`go test ./internal/webui/` 通过（新增 6 个用例，覆盖服务地址规范化、redirect 清洗、state 生命周期与重放、回调地址推导）。

用一个模拟 cf-worker-auth 的桩服务做了端到端实测，均符合预期：

- 完整流程：start → 认证服务 → 回调 → 种会话 → 跳回 `redirect` 指定的站内页面，`/api/auth/status` 返回 `user`。
- **token/口令登录不受影响**，错误口令仍然返回「口令错误」并计入限速。
- 伪造回调（无 state Cookie）、state 重放、认证服务返回 401 → 均 302 回登录页并带错误原因。
- `--auth-user` 白名单外的用户被拒，日志记录被拒用户名。
- `redirect=//evil.com` / `https://evil.com` 被清洗成站内首页。
- 伪造 `Host: evil.com` 时拒绝推导回调地址，返回 400 并提示用 `--external-url`。
- 已登录状态下访问 start 直接 302 回首页，不重复发起流程。

Docker 侧把 `entrypoint.sh` 的 `exec` 换成打印后逐组合模拟，参数拼装均正确：只有口令、只有认证服务、
两者并存、缺 `EXTERNAL_URL` 告警、两者都缺时退出码 1、只设 `AUTH_USERS` 未设 `AUTH_URL` 告警；
`AUTH_USERS` 里的空格与空项被正确清洗。`docker compose config` 通过。

未实测：真实 GitHub OAuth 往返（需要真实的 Worker 部署与 OAuth App）、容器内实跑。
