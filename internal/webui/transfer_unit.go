package webui

import (
	"time"

	"github.com/tickstep/aliyunpan/internal/taskframework"
)

// wrappedUnit 委托包装器。
//
// 这是整个 Web 传输功能零侵入的关键：taskframework.TaskUnit 是接口，
// pandownload.DownloadTaskUnit / panupload.UploadTaskUnit 的字段又全部导出，
// 于是我们可以在外面包一层，把 SetTaskInfo/Run/OnSuccess/... 这些生命周期切面
// 全部拦下来转成 Web 事件，而完全不用修改 internal/functions 和 internal/taskframework。
//
// 同时 Run() 在委托之前会先过一遍任务闸门，实现队列级的暂停与取消。
type wrappedUnit struct {
	inner taskframework.TaskUnit
	job   *Job

	id string
	// panPath 展示用路径（下载是网盘路径，上传是本地路径）
	panPath string
	// localPath 下载时的本地落盘路径，展示用
	localPath string
	size      int64
}

var _ taskframework.TaskUnit = (*wrappedUnit)(nil)

func (w *wrappedUnit) SetTaskInfo(info *taskframework.TaskInfo) {
	if info != nil {
		w.id = info.Id()
		w.job.registerTask(w.id, w.panPath, w.localPath, w.size)
	}
	w.inner.SetTaskInfo(info)
}

func (w *wrappedUnit) Run() (result *taskframework.TaskUnitRunResult) {
	// 队列级闸门：暂停时阻塞在这里，不占用 CPU；取消时直接跳过该文件
	w.job.gate.waitIfPaused()
	if w.job.gate.isCanceled() {
		w.job.markTask(w.id, TaskCanceled, "任务已取消")
		return &taskframework.TaskUnitRunResult{Cancel: true, ResultMessage: "任务已取消"}
	}

	w.job.markTask(w.id, TaskRunning, "")
	return w.inner.Run()
}

func (w *wrappedUnit) OnRetry(lastRunResult *taskframework.TaskUnitRunResult) {
	w.job.markTask(w.id, TaskRetrying, resultMessage(lastRunResult))
	w.inner.OnRetry(lastRunResult)
}

func (w *wrappedUnit) OnSuccess(lastRunResult *taskframework.TaskUnitRunResult) {
	w.job.markTask(w.id, TaskSuccess, "")
	w.inner.OnSuccess(lastRunResult)
}

func (w *wrappedUnit) OnFailed(lastRunResult *taskframework.TaskUnitRunResult) {
	w.job.markTask(w.id, TaskFailed, resultMessage(lastRunResult))
	w.inner.OnFailed(lastRunResult)
}

func (w *wrappedUnit) OnComplete(lastRunResult *taskframework.TaskUnitRunResult) {
	w.inner.OnComplete(lastRunResult)
}

func (w *wrappedUnit) OnCancel(lastRunResult *taskframework.TaskUnitRunResult) {
	w.job.markTask(w.id, TaskCanceled, resultMessage(lastRunResult))
	w.inner.OnCancel(lastRunResult)
}

func (w *wrappedUnit) RetryWait() time.Duration {
	return w.inner.RetryWait()
}

func resultMessage(r *taskframework.TaskUnitRunResult) string {
	if r == nil {
		return ""
	}
	msg := r.ResultMessage
	if r.Err != nil {
		if msg != "" {
			msg += ": "
		}
		msg += r.Err.Error()
	}
	return msg
}
