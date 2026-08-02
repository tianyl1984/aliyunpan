package webui

import (
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/tickstep/aliyunpan-api/aliyunpan"
	"github.com/tickstep/aliyunpan/cmder/cmdutil"
	"github.com/tickstep/aliyunpan/internal/command"
	"github.com/tickstep/aliyunpan/internal/config"
	"github.com/tickstep/aliyunpan/internal/functions/panupload"
	"github.com/tickstep/aliyunpan/internal/localfile"
	"github.com/tickstep/aliyunpan/internal/log"
	"github.com/tickstep/aliyunpan/internal/taskframework"
	"github.com/tickstep/aliyunpan/internal/ui"
	"github.com/tickstep/aliyunpan/internal/utils"
	"github.com/tickstep/library-go/logger"
)

// defaultUploadBlockSize 默认上传分片大小（10MB），与 CLI 的 -bs 默认值一致
const defaultUploadBlockSize = int64(10240) * 1024

// SubmitUpload 组装并启动一个上传任务。
//
// 参数装配逻辑同步自 internal/command/upload.go 的 RunUpload @ v0.4.0。
func (m *Manager) SubmitUpload(u *config.PanUser, spec *JobSpec) (*Job, error) {
	if len(spec.LocalPaths) == 0 {
		return nil, badRequest("localPaths 不能为空")
	}
	savePath := cleanPanPath(spec.PanDir)
	spec.PanDir = savePath

	allParallel := spec.Parallel
	if allParallel <= 0 {
		allParallel = config.Config.MaxUploadParallel
		if allParallel == 0 {
			allParallel = config.DefaultFileUploadParallelNum
		}
	}
	if allParallel > config.MaxFileUploadParallelNum {
		allParallel = config.MaxFileUploadParallelNum
	}
	maxRetry := spec.MaxRetry
	if maxRetry < 0 {
		maxRetry = command.DefaultUploadMaxRetry
	}
	blockSize := spec.BlockSize
	if blockSize <= 0 {
		blockSize = defaultUploadBlockSize
	}

	// 目标目录必须存在，不存在则创建
	client := u.PanClient().OpenapiPanClient()
	if savePath != "/" {
		if _, apiErr := client.FileInfoByPath(spec.DriveId, savePath); apiErr != nil {
			if _, mkErr := client.MkdirByFullPath(spec.DriveId, savePath); mkErr != nil {
				return nil, badRequest("网盘目标目录不存在且创建失败: " + savePath)
			}
		}
	}

	uploadDatabase, err := panupload.NewUploadingDatabase()
	if err != nil {
		return nil, internalError("打开上传断点数据库失败: " + err.Error())
	}

	job, jErr := m.newJob(spec, uploadTitle(spec.LocalPaths))
	if jErr != nil {
		uploadDatabase.Close()
		return nil, jErr
	}

	executor := &taskframework.TaskExecutor{IsFailedDeque: true}
	executor.SetParallel(allParallel)
	statistic := &panupload.UploadStatistic{}
	statistic.StartTimer()
	panel := silentPanel(ui.DashboardPanelUpload, allParallel, job)
	fileRecorder := log.NewFileRecorder(config.GetLogDir() + "/upload_file_records.csv")
	folderCreateMutex := &sync.Mutex{}

	appended := 0
	for _, curPath := range spec.LocalPaths {
		curPath = filepath.Clean(curPath)
		if utils.IsExcludeFile(curPath, &spec.ExcludeNames) {
			job.log("排除文件: " + curPath)
			continue
		}
		localPathDir := filepath.Dir(curPath)
		if localPathDir == "." {
			localPathDir = ""
		}

		walkFunc := func(file localfile.SymlinkFile, fi os.FileInfo, werr error) error {
			if werr != nil {
				logger.Verboseln("webui upload walk error: ", file, werr)
				return nil
			}
			if os.PathSeparator == '\\' {
				file.LogicPath = cmdutil.ConvertToWindowsPathSeparator(file.LogicPath)
				file.RealPath = cmdutil.ConvertToWindowsPathSeparator(file.RealPath)
			}
			if utils.IsExcludeFile(file.LogicPath, &spec.ExcludeNames) {
				job.log("排除文件: " + file.LogicPath)
				return filepath.SkipDir
			}

			subSavePath := strings.TrimPrefix(file.LogicPath, localPathDir)
			if os.PathSeparator == '\\' {
				subSavePath = cmdutil.ConvertToUnixPathSeparator(subSavePath)
			}
			subSavePath = path.Clean(savePath + aliyunpan.PathSeparator + subSavePath)

			if !fi.IsDir() {
				unit := &panupload.UploadTaskUnit{
					LocalFileChecksum: localfile.NewLocalSymlinkFileEntity(file),
					SavePath:          subSavePath,
					DriveId:           spec.DriveId,
					PanClient:         u.PanClient(),
					UploadingDatabase: uploadDatabase,
					FolderCreateMutex: folderCreateMutex,
					Parallel:          1,
					NoRapidUpload:     spec.NoRapidUpload,
					BlockSize:         blockSize,
					UploadStatistic:   statistic,
					ShowProgress:      false,
					IsOverwrite:       spec.IsOverwrite,
					GlobalSpeedsStat:  job.speeds,
					FileRecorder:      fileRecorder,
					UI:                panel,
				}
				executor.Append(&wrappedUnit{
					inner:   unit,
					job:     job,
					panPath: file.LogicPath,
					size:    fi.Size(),
				}, maxRetry)
				appended++
				return nil
			}

			// 目录：提前创建，保证空目录也能同步上去
			if subSavePath != "/" {
				folderCreateMutex.Lock()
				if _, apiErr := client.FileInfoByPath(spec.DriveId, subSavePath); apiErr != nil {
					if _, mkErr := client.MkdirByFullPath(spec.DriveId, subSavePath); mkErr != nil {
						job.log("创建云盘文件夹失败: " + subSavePath)
					}
				}
				folderCreateMutex.Unlock()
			}
			return nil
		}

		sf := localfile.NewSymlinkFile(curPath)
		if werr := localfile.WalkAllFile(sf, walkFunc); werr != nil && werr != filepath.SkipDir {
			job.log("遍历本地目录出错: " + werr.Error())
		}
	}

	if appended == 0 {
		uploadDatabase.Close()
		return nil, notFound("没有找到可上传的文件")
	}

	job.executor = executor
	job.statistic = statistic
	job.onFinish = func() {
		uploadDatabase.Close()
		cleanupStagePaths(spec.stagePaths)
	}
	m.register(job)
	m.runJob(job)
	return job, nil
}

func uploadTitle(paths []string) string {
	if len(paths) == 1 {
		return filepath.Base(paths[0])
	}
	return filepath.Base(paths[0]) + " 等 " + strconv.Itoa(len(paths)) + " 项"
}

func cleanupStagePaths(paths []string) {
	for _, p := range paths {
		if p == "" {
			continue
		}
		_ = os.RemoveAll(filepath.Dir(p))
	}
}
