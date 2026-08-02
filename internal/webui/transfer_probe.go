package webui

import (
	"os"
	"sync"
	"time"

	"github.com/tickstep/aliyunpan/internal/functions/pandownload"
)

// probeInterval 探针采样间隔
const probeInterval = 500 * time.Millisecond

// startProbe 为一个正在运行的文件启动字节级进度探针，返回停止函数。
//
// 下载器把数据先写到 SavePath + ".aliyunpan-downloading"，完成后才重命名。
// 定时 stat 这个临时文件就能拿到精确的已下载字节数，差分即得速率 —— 不需要
// 改动 pandownload 里的任何一行代码。
//
// 上传没有本地临时文件，拿不到单文件字节进度，此时探针直接返回空操作，
// 前端会退化为「状态 + 全局速率」的展示。
func (j *Job) startProbe(taskId string) func() {
	if j.Type != JobDownload {
		return func() {}
	}

	j.mu.Lock()
	t := j.tasks[taskId]
	var localPath string
	if t != nil {
		localPath = t.LocalPath
	}
	j.mu.Unlock()

	if localPath == "" {
		return func() {}
	}

	tmpPath := localPath + pandownload.DownloadSuffix
	stop := make(chan struct{})
	var once sync.Once

	go func() {
		ticker := time.NewTicker(probeInterval)
		defer ticker.Stop()

		var (
			lastSize int64
			lastAt   = time.Now()
		)
		for {
			select {
			case <-stop:
				return
			case <-j.probeStop:
				return
			case now := <-ticker.C:
				size, ok := fileSize(tmpPath)
				if !ok {
					// 临时文件还没建，或者已经重命名成最终文件
					if s, ok2 := fileSize(localPath); ok2 {
						size = s
					} else {
						continue
					}
				}
				elapsed := now.Sub(lastAt).Seconds()
				var speed int64
				if elapsed > 0 && size >= lastSize {
					speed = int64(float64(size-lastSize) / elapsed)
				}
				lastSize, lastAt = size, now
				j.updateTaskProgress(taskId, size, speed)
			}
		}
	}()

	return func() { once.Do(func() { close(stop) }) }
}

func fileSize(p string) (int64, bool) {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return 0, false
	}
	return fi.Size(), true
}

// stopAllProbes 任务结束时关闭所有探针协程
func (j *Job) stopAllProbes() {
	j.probeOnce.Do(func() { close(j.probeStop) })
}
