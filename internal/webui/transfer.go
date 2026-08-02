package webui

import (
	"sort"
	"sync"
	"time"

	"github.com/tickstep/aliyunpan/internal/taskframework"
	"github.com/tickstep/library-go/requester/rio/speeds"
)

// JobType 传输任务类型
type JobType string

const (
	JobDownload JobType = "download"
	JobUpload   JobType = "upload"
)

// JobState 任务状态
type JobState string

const (
	JobQueued    JobState = "queued"
	JobRunning   JobState = "running"
	JobPaused    JobState = "paused"
	JobCompleted JobState = "completed"
	JobFailed    JobState = "failed"
	JobCanceled  JobState = "canceled"
)

// 单个文件的状态
const (
	TaskQueued   = "queued"
	TaskRunning  = "running"
	TaskSuccess  = "success"
	TaskFailed   = "failed"
	TaskCanceled = "canceled"
	TaskRetrying = "retrying"
)

// progressThrottle 单文件字节进度的最小推送间隔。
// 传输器每约 200ms 触发一次状态回调，多文件并发会打爆 SSE，必须节流。
const progressThrottle = 500 * time.Millisecond

// maxKeepJobs 内存中保留的终态任务数量上限
const maxKeepJobs = 200

// gate 任务闸门。
//
// taskframework.TaskExecutor 的 Pause/Resume/Stop 是空实现，但我们不需要去改它：
// 委托包装器在把执行权交给真正的 TaskUnit 之前先过这道闸门，等价于队列级的暂停/取消。
// 代价是暂停只在文件边界生效——正在传输的文件会跑完。
type gate struct {
	mu       sync.Mutex
	cond     *sync.Cond
	paused   bool
	canceled bool
}

func newGate() *gate {
	g := &gate{}
	g.cond = sync.NewCond(&g.mu)
	return g
}

func (g *gate) waitIfPaused() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for g.paused && !g.canceled {
		g.cond.Wait()
	}
}

func (g *gate) isCanceled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.canceled
}

func (g *gate) isPaused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused
}

func (g *gate) pause() {
	g.mu.Lock()
	g.paused = true
	g.mu.Unlock()
}

func (g *gate) resume() {
	g.mu.Lock()
	g.paused = false
	g.mu.Unlock()
	g.cond.Broadcast()
}

func (g *gate) cancel() {
	g.mu.Lock()
	g.canceled = true
	g.paused = false
	g.mu.Unlock()
	g.cond.Broadcast()
}

// taskRec 单个文件的传输记录
type taskRec struct {
	Id string `json:"id"`
	// Path 网盘路径（下载）或本地路径（上传）
	Path string `json:"path"`
	// LocalPath 下载时的本地落盘路径
	LocalPath string `json:"localPath,omitempty"`
	Size      int64  `json:"size"`
	Done      int64  `json:"done"`
	Speed     int64  `json:"speed"`
	State     string `json:"state"`
	Message   string `json:"message,omitempty"`
}

// JobSpec 提交一个传输任务的参数
type JobSpec struct {
	Type    JobType
	DriveId string
	// 下载：网盘源路径列表；上传：网盘目标目录
	PanPaths []string
	PanDir   string
	// 下载：本地保存目录；上传：本地源路径列表
	SaveTo     string
	LocalPaths []string

	// 传输参数，0 表示使用配置文件/默认值
	Parallel      int
	SliceParallel int
	MaxRetry      int
	BlockSize     int64
	IsOverwrite   bool
	NoRapidUpload bool
	ExcludeNames  []string

	// stagePaths 浏览器直传产生的暂存文件，任务结束后需要清理
	stagePaths []string
}

// Job 一次传输任务
type Job struct {
	Id        string  `json:"id"`
	Type      JobType `json:"type"`
	DriveId   string  `json:"driveId"`
	Title     string  `json:"title"`
	SaveTo    string  `json:"saveTo,omitempty"`
	PanDir    string  `json:"panDir,omitempty"`
	CreatedAt int64   `json:"createdAt"`

	mu         sync.Mutex
	state      JobState
	message    string
	startedAt  int64
	finishedAt int64
	tasks      map[string]*taskRec
	order      []string
	lastPub    map[string]int64

	gate       *gate
	speeds     *speeds.Speeds
	statistic  interface{ TotalSize() int64 }
	executor   *taskframework.TaskExecutor
	spec       *JobSpec
	mgr        *Manager
	cancelOnce sync.Once
	// onFinish 任务彻底结束后的清理钩子（关闭断点数据库、删除暂存文件等）
	onFinish func()
}

// JobSnapshot 返回给前端的任务快照
type JobSnapshot struct {
	Id          string     `json:"id"`
	Type        JobType    `json:"type"`
	DriveId     string     `json:"driveId"`
	Title       string     `json:"title"`
	SaveTo      string     `json:"saveTo,omitempty"`
	PanDir      string     `json:"panDir,omitempty"`
	State       JobState   `json:"state"`
	Message     string     `json:"message,omitempty"`
	CreatedAt   int64      `json:"createdAt"`
	StartedAt   int64      `json:"startedAt,omitempty"`
	FinishedAt  int64      `json:"finishedAt,omitempty"`
	FilesTotal  int        `json:"filesTotal"`
	FilesDone   int        `json:"filesDone"`
	FilesFailed int        `json:"filesFailed"`
	BytesTotal  int64      `json:"bytesTotal"`
	BytesDone   int64      `json:"bytesDone"`
	Speed       int64      `json:"speed"`
	Tasks       []*taskRec `json:"tasks,omitempty"`
}

// Manager 传输任务管理器
type Manager struct {
	mu     sync.Mutex
	jobs   map[string]*Job
	order  []string
	events *EventBroker
	logf   func(string, ...interface{})
}

func NewManager(events *EventBroker, logf func(string, ...interface{})) *Manager {
	return &Manager{
		jobs:   make(map[string]*Job),
		order:  []string{},
		events: events,
		logf:   logf,
	}
}

func (m *Manager) newJob(spec *JobSpec, title string) (*Job, error) {
	id, err := randomHex(8)
	if err != nil {
		return nil, internalError("生成任务ID失败: " + err.Error())
	}
	j := &Job{
		Id:        id,
		Type:      spec.Type,
		DriveId:   spec.DriveId,
		Title:     title,
		SaveTo:    spec.SaveTo,
		PanDir:    spec.PanDir,
		CreatedAt: time.Now().UnixMilli(),
		state:     JobQueued,
		tasks:     make(map[string]*taskRec),
		order:     []string{},
		lastPub:   make(map[string]int64),
		gate:      newGate(),
		speeds:    &speeds.Speeds{},
		spec:      spec,
		mgr:       m,
	}
	return j, nil
}

func (m *Manager) register(j *Job) {
	m.mu.Lock()
	m.jobs[j.Id] = j
	m.order = append(m.order, j.Id)
	m.trimLocked()
	m.mu.Unlock()

	m.events.Publish(Event{Type: EventJobAdded, Data: j.Snapshot(false)})
}

// trimLocked 淘汰最旧的终态任务，保持内存占用有上限
func (m *Manager) trimLocked() {
	if len(m.order) <= maxKeepJobs {
		return
	}
	keep := make([]string, 0, len(m.order))
	removed := 0
	need := len(m.order) - maxKeepJobs
	for _, id := range m.order {
		j := m.jobs[id]
		if removed < need && j != nil && j.isTerminal() {
			delete(m.jobs, id)
			removed++
			continue
		}
		keep = append(keep, id)
	}
	m.order = keep
}

func (m *Manager) Get(id string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, notFound("任务不存在: " + id)
	}
	return j, nil
}

func (m *Manager) List(typeFilter, stateFilter string) []*JobSnapshot {
	m.mu.Lock()
	jobs := make([]*Job, 0, len(m.order))
	for _, id := range m.order {
		if j := m.jobs[id]; j != nil {
			jobs = append(jobs, j)
		}
	}
	m.mu.Unlock()

	out := make([]*JobSnapshot, 0, len(jobs))
	for _, j := range jobs {
		snap := j.Snapshot(false)
		if typeFilter != "" && string(snap.Type) != typeFilter {
			continue
		}
		if stateFilter != "" && string(snap.State) != stateFilter {
			continue
		}
		out = append(out, snap)
	}
	// 新任务排在前面
	sort.SliceStable(out, func(i, k int) bool { return out[i].CreatedAt > out[k].CreatedAt })
	return out
}

// Remove 删除一条终态任务记录
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return notFound("任务不存在: " + id)
	}
	if !j.isTerminal() {
		return badRequest("任务尚未结束，请先取消")
	}
	delete(m.jobs, id)
	for i, v := range m.order {
		if v == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	return nil
}

// Clear 清空所有终态任务记录
func (m *Manager) Clear() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	keep := make([]string, 0, len(m.order))
	n := 0
	for _, id := range m.order {
		j := m.jobs[id]
		if j != nil && j.isTerminal() {
			delete(m.jobs, id)
			n++
			continue
		}
		keep = append(keep, id)
	}
	m.order = keep
	return n
}

// Shutdown 取消所有进行中的任务，等待它们退出（断点会自动落盘）
func (m *Manager) Shutdown(timeout time.Duration) {
	m.mu.Lock()
	jobs := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.mu.Unlock()

	for _, j := range jobs {
		if !j.isTerminal() {
			j.gate.cancel()
		}
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		busy := false
		for _, j := range jobs {
			if !j.isTerminal() {
				busy = true
				break
			}
		}
		if !busy {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ---- Job 状态操作 ----

func (j *Job) isTerminal() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	switch j.state {
	case JobCompleted, JobFailed, JobCanceled:
		return true
	}
	return false
}

func (j *Job) setState(st JobState, msg string) {
	j.mu.Lock()
	if j.state == st && j.message == msg {
		j.mu.Unlock()
		return
	}
	j.state = st
	j.message = msg
	if st == JobRunning && j.startedAt == 0 {
		j.startedAt = time.Now().UnixMilli()
	}
	switch st {
	case JobCompleted, JobFailed, JobCanceled:
		j.finishedAt = time.Now().UnixMilli()
	}
	j.mu.Unlock()

	j.mgr.events.Publish(Event{Type: EventJobState, Data: j.Snapshot(false)})
}

func (j *Job) Pause() error {
	if j.isTerminal() {
		return badRequest("任务已结束，无法暂停")
	}
	j.gate.pause()
	j.setState(JobPaused, "已暂停，正在传输的文件会先跑完")
	return nil
}

func (j *Job) Resume() error {
	if j.isTerminal() {
		return badRequest("任务已结束，无法继续")
	}
	if !j.gate.isPaused() {
		return nil
	}
	j.gate.resume()
	j.setState(JobRunning, "")
	return nil
}

func (j *Job) Cancel() error {
	if j.isTerminal() {
		return badRequest("任务已结束")
	}
	j.cancelOnce.Do(func() { j.gate.cancel() })
	// 这里刻意不直接置成终态：正在传输的文件还要跑完，执行器退出后
	// runJob 才会写入最终状态。提前置终态会让 Shutdown/Remove 误判任务已结束。
	j.setState(JobRunning, "正在取消，等待当前文件结束…")
	return nil
}

// ---- 进度记录（作为委托包装器的 sink） ----

func (j *Job) registerTask(id, panPath, localPath string, size int64) {
	j.mu.Lock()
	if _, ok := j.tasks[id]; !ok {
		j.order = append(j.order, id)
	}
	j.tasks[id] = &taskRec{
		Id: id, Path: panPath, LocalPath: localPath, Size: size, State: TaskQueued,
	}
	j.mu.Unlock()
}

func (j *Job) markTask(id, state, msg string) {
	j.mu.Lock()
	t := j.tasks[id]
	if t == nil {
		j.mu.Unlock()
		return
	}
	t.State = state
	t.Message = msg
	if state == TaskSuccess {
		t.Done = t.Size
		t.Speed = 0
	}
	if state == TaskFailed || state == TaskCanceled {
		t.Speed = 0
	}
	snap := *t
	j.mu.Unlock()

	j.mgr.events.Publish(Event{Type: EventTaskState, Data: map[string]interface{}{
		"jobId": j.Id, "task": &snap,
	}})
	j.mgr.events.Publish(Event{Type: EventJobProgress, Data: j.Snapshot(false)})
}

// onPanelProgress 统计面板的进度钩子。
//
// 上传/下载任务单元本来就会把 (已传字节, 总字节, 速率) 报给 ui.DashboardPanel，
// 这里直接借道同一份数据，无需另外去猜本地文件大小。
func (j *Job) onPanelProgress(id string, done, total, speed int64, _ time.Duration) {
	j.updateTaskProgress(id, done, total, speed)
}

// updateTaskProgress 更新单文件字节进度，带节流。total <= 0 表示总大小未知，沿用注册时的值。
func (j *Job) updateTaskProgress(id string, done, total, speed int64) {
	now := time.Now().UnixMilli()

	j.mu.Lock()
	t := j.tasks[id]
	if t == nil {
		j.mu.Unlock()
		return
	}
	if total > 0 {
		t.Size = total
	}
	if t.Size > 0 && done > t.Size {
		done = t.Size
	}
	t.Done = done
	t.Speed = speed
	last := j.lastPub[id]
	if now-last < progressThrottle.Milliseconds() {
		j.mu.Unlock()
		return
	}
	j.lastPub[id] = now
	snap := *t
	j.mu.Unlock()

	j.mgr.events.Publish(Event{Type: EventTaskProgress, Data: map[string]interface{}{
		"jobId": j.Id, "task": &snap,
	}})
	// 同时刷新任务级进度，否则单个大文件传输期间整体进度条不会动
	j.mgr.events.Publish(Event{Type: EventJobProgress, Data: j.Snapshot(false)})
}

func (j *Job) log(msg string) {
	j.mgr.events.Publish(Event{Type: EventLog, Data: map[string]interface{}{
		"jobId": j.Id, "text": msg,
	}})
}

// Snapshot 生成任务快照。withTasks 为 true 时附带每个文件的明细。
func (j *Job) Snapshot(withTasks bool) *JobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()

	s := &JobSnapshot{
		Id:         j.Id,
		Type:       j.Type,
		DriveId:    j.DriveId,
		Title:      j.Title,
		SaveTo:     j.SaveTo,
		PanDir:     j.PanDir,
		State:      j.state,
		Message:    j.message,
		CreatedAt:  j.CreatedAt,
		StartedAt:  j.startedAt,
		FinishedAt: j.finishedAt,
	}
	for _, id := range j.order {
		t := j.tasks[id]
		if t == nil {
			continue
		}
		s.FilesTotal++
		s.BytesTotal += t.Size
		switch t.State {
		case TaskSuccess:
			s.FilesDone++
			s.BytesDone += t.Size
		case TaskFailed, TaskCanceled:
			s.FilesFailed++
		default:
			s.BytesDone += t.Done
		}
		if withTasks {
			c := *t
			s.Tasks = append(s.Tasks, &c)
		}
	}
	if j.speeds != nil && (j.state == JobRunning || j.state == JobPaused) {
		s.Speed = j.speeds.GetSpeeds()
	}
	return s
}
