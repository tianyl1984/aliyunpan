package webui

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/tickstep/aliyunpan/internal/command"
	"github.com/tickstep/aliyunpan/internal/config"
	"github.com/tickstep/aliyunpan/internal/file/downloader"
	"github.com/tickstep/aliyunpan/internal/functions/pandownload"
	"github.com/tickstep/aliyunpan/internal/global"
	"github.com/tickstep/aliyunpan/internal/log"
	"github.com/tickstep/aliyunpan/internal/taskframework"
	"github.com/tickstep/aliyunpan/internal/ui"
	"github.com/tickstep/aliyunpan/internal/utils"
	"github.com/tickstep/aliyunpan/library/requester/transfer"
	"github.com/tickstep/library-go/requester/rio/speeds"
)

// silentPanel 构造一个「哑」的统计面板。
//
// pandownload/panupload 的任务单元在 UI 字段非 nil 时会把日志与进度都交给面板，
// 完全不写 os.Stdout（见 download_task_unit.go 的 OnDownloadStatusEvent 与 logf）。
// 我们构造一个输出到 io.Discard 且从不调用 Start() 的面板：它不会渲染、不会起协程，
// 只是把服务端进程的标准输出保持干净——这样网页控制台捕获 stdout 时不会混入传输日志。
//
// 同时挂上 OnProgress 钩子，把任务单元汇报给面板的字节级进度原样转发给 Job，
// 这是网页端单文件进度条的唯一数据来源（上传与下载通用）。
func silentPanel(t ui.DashboardType, parallel int, sp *speeds.Speeds, onProgress func(string, int64, int64, int64, time.Duration)) *ui.DashboardPanel {
	return ui.NewDashboardPanel(t, parallel, sp, &ui.DashboardOptions{
		Title:      "webui",
		MaxHistory: 1,
		Output:     io.Discard,
		OnProgress: onProgress,
	})
}

// SubmitDownload 组装并启动一个下载任务。
//
// 参数装配逻辑同步自 internal/command/download.go 的 RunDownload @ v0.4.0。
// 上游若调整下载并发策略或 downloader.Config 字段，需要同步这里。
func (m *Manager) SubmitDownload(u *config.PanUser, spec *JobSpec) (*Job, error) {
	if len(spec.PanPaths) == 0 {
		return nil, badRequest("panPaths 不能为空")
	}

	// 保存根目录
	saveRoot := spec.SaveTo
	if saveRoot == "" {
		saveRoot = u.GetSavePath("")
	}
	if fi, err := os.Stat(saveRoot); err != nil {
		if mkErr := os.MkdirAll(saveRoot, 0755); mkErr != nil {
			return nil, badRequest("创建本地保存目录失败: " + mkErr.Error())
		}
	} else if !fi.IsDir() {
		return nil, badRequest("本地保存路径不是文件夹: " + saveRoot)
	}
	spec.SaveTo = saveRoot

	// 并发参数。阿里云盘对下载并发有风控，这里沿用 CLI 的钳制逻辑。
	parallel := spec.Parallel
	if parallel < 1 {
		parallel = config.Config.MaxDownloadParallel
		if parallel == 0 {
			parallel = config.DefaultFileDownloadParallelNum
		}
	}
	if parallel > config.MaxFileDownloadParallelNum {
		parallel = config.MaxFileDownloadParallelNum
	}
	sliceParallel := spec.SliceParallel
	if sliceParallel < 1 {
		if parallel > 1 {
			sliceParallel = 1
		} else {
			sliceParallel = 3
		}
	}
	maxRetry := spec.MaxRetry
	if maxRetry < 0 {
		maxRetry = pandownload.DefaultDownloadMaxRetry
	}

	cacheSize := config.Config.CacheSize
	if cacheSize == 0 {
		cacheSize = int(command.DownloadCacheSize)
	}

	job, err := m.newJob(spec, downloadTitle(spec.PanPaths))
	if err != nil {
		return nil, err
	}

	cfg := &downloader.Config{
		Mode:                       transfer.RangeGenMode_BlockSize,
		CacheSize:                  cacheSize,
		BlockSize:                  command.MaxDownloadRangeSize,
		MaxRate:                    config.Config.MaxDownloadRate,
		InstanceStateStorageFormat: downloader.InstanceStateStorageFormatJSON,
		ShowProgress:               false,
		ExcludeNames:               spec.ExcludeNames,
		MaxParallel:                parallel,
		SliceParallel:              sliceParallel,
	}

	executor := &taskframework.TaskExecutor{IsFailedDeque: true}
	executor.SetParallel(parallel)
	statistic := &pandownload.DownloadStatistic{}
	statistic.StartTimer()
	panel := silentPanel(ui.DashboardPanelDownload, parallel, job.speeds, job.onPanelProgress)
	fileRecorder := log.NewFileRecorder(config.GetLogDir() + "/download_file_records.csv")

	client := u.PanClient().OpenapiPanClient()
	appended := 0
	for _, p := range spec.PanPaths {
		absPath := cleanPanPath(p)
		matched, apiErr := client.MatchPathByShellPattern(spec.DriveId, absPath)
		if apiErr != nil || matched == nil || len(*matched) == 0 {
			job.log("文件不存在或获取失败: " + absPath)
			continue
		}
		fileList := *matched
		sort.Slice(fileList, func(i, k int) bool { return fileList[i].FileName < fileList[k].FileName })

		for _, f := range fileList {
			if utils.IsExcludeFile(f.Path, &cfg.ExcludeNames) {
				job.log("排除文件: " + f.Path)
				continue
			}
			newCfg := *cfg
			unit := &pandownload.DownloadTaskUnit{
				DownloadActionId:     job.Id,
				Cfg:                  &newCfg,
				PanClient:            u.PanClient(),
				ParentTaskExecutor:   executor,
				DownloadStatistic:    statistic,
				IsPrintStatus:        false,
				IsExecutedPermission: false,
				IsOverwrite:          spec.IsOverwrite,
				NoCheck:              false,
				FilePanSource:        global.FileSource,
				FilePanPath:          f.Path,
				DriveId:              spec.DriveId,
				GlobalSpeedsStat:     job.speeds,
				FileRecorder:         fileRecorder,
				UI:                   panel,
			}
			if spec.SaveTo != "" {
				unit.OriginSaveRootPath = spec.SaveTo
				unit.SavePath = filepath.Join(spec.SaveTo, f.Path)
			} else {
				unit.OriginSaveRootPath = u.GetSavePath("")
				unit.SavePath = u.GetSavePath(f.Path)
			}

			executor.Append(&wrappedUnit{
				inner:     unit,
				job:       job,
				panPath:   f.Path,
				localPath: unit.SavePath,
				size:      f.FileSize,
			}, maxRetry)
			appended++
		}
	}

	if appended == 0 {
		return nil, notFound("没有匹配到可下载的文件")
	}

	job.executor = executor
	job.statistic = statistic
	m.register(job)
	m.runJob(job)
	return job, nil
}

func downloadTitle(paths []string) string {
	if len(paths) == 1 {
		return filepath.Base(cleanPanPath(paths[0]))
	}
	return filepath.Base(cleanPanPath(paths[0])) + " 等 " + strconv.Itoa(len(paths)) + " 项"
}
