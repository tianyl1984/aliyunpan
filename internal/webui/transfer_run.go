package webui

import (
	"fmt"
	"time"

	"github.com/tickstep/aliyunpan/internal/config"
)

// runJob 在独立协程里跑执行器。
// taskframework.TaskExecutor.Execute() 会阻塞到队列清空，包一层协程即可。
func (m *Manager) runJob(j *Job) {
	j.setState(JobRunning, "")

	go func() {
		defer func() {
			if r := recover(); r != nil {
				m.logf("传输任务 %s 发生 panic: %v", j.Id, r)
				j.setState(JobFailed, fmt.Sprintf("内部错误: %v", r))
			}
			j.stopAllProbes()
			if j.onFinish != nil {
				j.onFinish()
			}
		}()

		j.executor.Execute()

		// 收尾：区分取消 / 失败 / 成功
		failed := 0
		if fd := j.executor.FailedDeque(); fd != nil {
			failed = fd.Size()
		}
		switch {
		case j.gate.isCanceled():
			j.setState(JobCanceled, "任务已取消")
		case failed > 0:
			j.setState(JobFailed, fmt.Sprintf("有 %d 个文件传输失败", failed))
		default:
			elapsed := time.Duration(0)
			if j.statistic != nil {
				if e, ok := j.statistic.(interface{ Elapsed() time.Duration }); ok {
					elapsed = e.Elapsed()
				}
			}
			msg := "传输完成"
			if elapsed > 0 {
				msg = fmt.Sprintf("传输完成，耗时 %s", elapsed.Truncate(time.Second))
			}
			j.setState(JobCompleted, msg)
		}
	}()
}

// Retry 重新提交一个已结束的任务
func (m *Manager) Retry(user *config.PanUser, id string) (*Job, error) {
	old, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if !old.isTerminal() {
		return nil, badRequest("任务尚未结束，无法重试")
	}
	if len(old.spec.stagePaths) > 0 {
		// 浏览器直传的暂存文件在任务结束时已被删除，没有源可重传
		return nil, badRequest("浏览器直传的任务无法重试，请重新选择文件上传")
	}

	// 复制一份规格重新提交。断点续传由 pandownload/panupload 自身保证：
	// 下载复用 .aliyunpan-downloading 临时文件，上传复用断点数据库与秒传。
	spec := *old.spec
	spec.stagePaths = nil
	if spec.Type == JobDownload {
		return m.SubmitDownload(user, &spec)
	}
	return m.SubmitUpload(user, &spec)
}
