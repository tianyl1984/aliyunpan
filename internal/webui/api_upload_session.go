package webui

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tickstep/aliyunpan/internal/config"
)

const (
	// stageDirName 浏览器直传的暂存目录
	stageDirName = "webui_stage"
	// defaultChunkSize 建议前端使用的分片大小
	defaultChunkSize = int64(8) << 20
	// maxBrowserUploadSize 浏览器直传的单文件上限。
	// 直传必须先在服务器落盘（秒传要算整文件 SHA1，分片上传要能重复读），
	// 超过这个体积建议改用「选择服务器本地文件」的方式。
	maxBrowserUploadSize = int64(20) << 30
	// stageTTL 暂存文件的最长保留时间
	stageTTL = 24 * time.Hour
)

type uploadSession struct {
	Id        string
	FileName  string
	Size      int64
	DriveId   string
	PanDir    string
	Overwrite bool
	StagePath string
	CreatedAt time.Time

	mu       sync.Mutex
	file     *os.File
	received int64
}

type uploadSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*uploadSession
}

func newUploadSessionStore() *uploadSessionStore {
	return &uploadSessionStore{sessions: make(map[string]*uploadSession)}
}

func stageRoot() string {
	return filepath.Join(config.GetConfigDir(), stageDirName)
}

func (st *uploadSessionStore) get(id string) (*uploadSession, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, ok := st.sessions[id]
	if !ok {
		return nil, notFound("上传会话不存在或已过期: " + id)
	}
	return s, nil
}

func (st *uploadSessionStore) remove(id string) {
	st.mu.Lock()
	s := st.sessions[id]
	delete(st.sessions, id)
	st.mu.Unlock()
	if s != nil {
		s.close()
	}
}

func (s *uploadSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		s.file.Close()
		s.file = nil
	}
}

// gcExpired 清理超时未完成的暂存会话
func (st *uploadSessionStore) gcExpired() {
	st.mu.Lock()
	expired := []*uploadSession{}
	for id, s := range st.sessions {
		if time.Since(s.CreatedAt) > stageTTL {
			expired = append(expired, s)
			delete(st.sessions, id)
		}
	}
	st.mu.Unlock()

	for _, s := range expired {
		s.close()
		_ = os.RemoveAll(filepath.Dir(s.StagePath))
	}
}

type createSessionRequest struct {
	DriveId   string `json:"driveId"`
	PanDir    string `json:"panDir"`
	FileName  string `json:"fileName"`
	Size      int64  `json:"size"`
	Overwrite bool   `json:"overwrite"`
}

func (s *Server) handleUploadSessionCreate(w http.ResponseWriter, r *http.Request) error {
	var req createSessionRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	name := strings.TrimSpace(req.FileName)
	if name == "" {
		return badRequest("fileName 不能为空")
	}
	// 只取基名，绝不用客户端给的名字拼服务器路径
	name = filepath.Base(filepath.FromSlash(name))
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return badRequest("非法的文件名")
	}
	if req.Size <= 0 {
		return badRequest("size 必须大于 0")
	}
	if req.Size > maxBrowserUploadSize {
		return badRequest("浏览器直传单文件上限为 20GB，更大的文件请使用「服务器本地文件」方式上传")
	}

	u, err := activeUser()
	if err != nil {
		return err
	}
	driveId, err := resolveDriveId(u, req.DriveId)
	if err != nil {
		return err
	}

	id, err := randomHex(16)
	if err != nil {
		return internalError("生成会话ID失败: " + err.Error())
	}
	dir := filepath.Join(stageRoot(), id)
	if mkErr := os.MkdirAll(dir, 0700); mkErr != nil {
		return internalError("创建暂存目录失败: " + mkErr.Error())
	}
	stagePath := filepath.Join(dir, name)

	f, fErr := os.OpenFile(stagePath, os.O_CREATE|os.O_RDWR, 0600)
	if fErr != nil {
		_ = os.RemoveAll(dir)
		return internalError("创建暂存文件失败: " + fErr.Error())
	}
	// 预分配，保证乱序分片可以直接 WriteAt
	if tErr := f.Truncate(req.Size); tErr != nil {
		f.Close()
		_ = os.RemoveAll(dir)
		return internalError("预分配暂存文件失败: " + tErr.Error())
	}

	sess := &uploadSession{
		Id:        id,
		FileName:  name,
		Size:      req.Size,
		DriveId:   driveId,
		PanDir:    cleanPanPath(req.PanDir),
		Overwrite: req.Overwrite,
		StagePath: stagePath,
		CreatedAt: time.Now(),
		file:      f,
	}
	s.uploads.mu.Lock()
	s.uploads.sessions[id] = sess
	s.uploads.mu.Unlock()

	return writeOKErr(w, map[string]interface{}{
		"uploadId":  id,
		"chunkSize": defaultChunkSize,
	})
}

func (s *Server) handleUploadSessionChunk(w http.ResponseWriter, r *http.Request) error {
	sess, err := s.uploads.get(r.PathValue("id"))
	if err != nil {
		return err
	}
	offsetStr := r.URL.Query().Get("offset")
	indexStr := r.URL.Query().Get("index")

	var offset int64
	switch {
	case offsetStr != "":
		v, e := strconv.ParseInt(offsetStr, 10, 64)
		if e != nil || v < 0 {
			return badRequest("offset 非法")
		}
		offset = v
	case indexStr != "":
		v, e := strconv.ParseInt(indexStr, 10, 64)
		if e != nil || v < 0 {
			return badRequest("index 非法")
		}
		offset = v * defaultChunkSize
	default:
		return badRequest("必须提供 offset 或 index")
	}
	if offset >= sess.Size {
		return badRequest("offset 超出文件大小")
	}

	body, readErr := io.ReadAll(io.LimitReader(r.Body, sess.Size-offset))
	if readErr != nil {
		return badRequest("读取分片失败: " + readErr.Error())
	}
	if len(body) == 0 {
		return badRequest("分片内容为空")
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.file == nil {
		return badRequest("会话已关闭")
	}
	if _, wErr := sess.file.WriteAt(body, offset); wErr != nil {
		return internalError("写入暂存文件失败: " + wErr.Error())
	}
	sess.received += int64(len(body))

	return writeOKErr(w, map[string]interface{}{
		"uploadId": sess.Id,
		"received": sess.received,
		"size":     sess.Size,
	})
}

func (s *Server) handleUploadSessionComplete(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	sess, err := s.uploads.get(id)
	if err != nil {
		return err
	}

	sess.mu.Lock()
	if sess.file != nil {
		_ = sess.file.Sync()
		sess.file.Close()
		sess.file = nil
	}
	received := sess.received
	sess.mu.Unlock()

	if received < sess.Size {
		return badRequest("分片未全部上传完成: " +
			strconv.FormatInt(received, 10) + "/" + strconv.FormatInt(sess.Size, 10))
	}
	if fi, statErr := os.Stat(sess.StagePath); statErr != nil || fi.Size() != sess.Size {
		return internalError("暂存文件校验失败")
	}

	u, uErr := activeUser()
	if uErr != nil {
		return uErr
	}

	spec := &JobSpec{
		Type:        JobUpload,
		DriveId:     sess.DriveId,
		PanDir:      sess.PanDir,
		LocalPaths:  []string{sess.StagePath},
		IsOverwrite: sess.Overwrite,
		stagePaths:  []string{sess.StagePath},
	}
	job, jErr := s.transfer.SubmitUpload(u, spec)
	if jErr != nil {
		return jErr
	}

	// 会话已交给传输任务，暂存文件的清理由任务结束钩子负责
	s.uploads.mu.Lock()
	delete(s.uploads.sessions, id)
	s.uploads.mu.Unlock()

	return writeOKErr(w, job.Snapshot(false))
}

func (s *Server) handleUploadSessionDelete(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	sess, err := s.uploads.get(id)
	if err != nil {
		return err
	}
	s.uploads.remove(id)
	_ = os.RemoveAll(filepath.Dir(sess.StagePath))
	return writeOKErr(w, map[string]string{"uploadId": id})
}
