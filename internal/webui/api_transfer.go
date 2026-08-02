package webui

import (
	"net/http"
	"strings"
)

func (s *Server) handleTransferJobs(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	return writeOKErr(w, map[string]interface{}{
		"jobs": s.transfer.List(strings.TrimSpace(q.Get("type")), strings.TrimSpace(q.Get("state"))),
	})
}

func (s *Server) handleTransferJobGet(w http.ResponseWriter, r *http.Request) error {
	j, err := s.transfer.Get(r.PathValue("id"))
	if err != nil {
		return err
	}
	return writeOKErr(w, j.Snapshot(true))
}

type downloadRequest struct {
	DriveId       string   `json:"driveId"`
	PanPaths      []string `json:"panPaths"`
	SaveTo        string   `json:"saveTo"`
	Parallel      int      `json:"parallel"`
	SliceParallel int      `json:"sliceParallel"`
	MaxRetry      int      `json:"maxRetry"`
	IsOverwrite   bool     `json:"isOverwrite"`
	ExcludeNames  []string `json:"excludeNames"`
}

func (s *Server) handleTransferDownload(w http.ResponseWriter, r *http.Request) error {
	var req downloadRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	u, err := activeUser()
	if err != nil {
		return err
	}
	driveId, err := resolveDriveId(u, req.DriveId)
	if err != nil {
		return err
	}
	if len(req.PanPaths) == 0 {
		return badRequest("panPaths 不能为空")
	}

	// 保存目录必须落在白名单内，防止把文件写到任意位置
	saveTo := strings.TrimSpace(req.SaveTo)
	if saveTo != "" {
		resolved, e := s.resolveLocalPath(saveTo)
		if e != nil {
			return e
		}
		saveTo = resolved
	}

	spec := &JobSpec{
		Type:          JobDownload,
		DriveId:       driveId,
		PanPaths:      req.PanPaths,
		SaveTo:        saveTo,
		Parallel:      req.Parallel,
		SliceParallel: req.SliceParallel,
		MaxRetry:      req.MaxRetry,
		IsOverwrite:   req.IsOverwrite,
		ExcludeNames:  req.ExcludeNames,
	}
	job, err := s.transfer.SubmitDownload(u, spec)
	if err != nil {
		return err
	}
	return writeOKErr(w, job.Snapshot(false))
}

type uploadRequest struct {
	DriveId       string   `json:"driveId"`
	PanDir        string   `json:"panDir"`
	LocalPaths    []string `json:"localPaths"`
	Parallel      int      `json:"parallel"`
	MaxRetry      int      `json:"maxRetry"`
	BlockSize     int64    `json:"blockSize"`
	IsOverwrite   bool     `json:"isOverwrite"`
	NoRapidUpload bool     `json:"noRapidUpload"`
	ExcludeNames  []string `json:"excludeNames"`
}

func (s *Server) handleTransferUpload(w http.ResponseWriter, r *http.Request) error {
	var req uploadRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	u, err := activeUser()
	if err != nil {
		return err
	}
	driveId, err := resolveDriveId(u, req.DriveId)
	if err != nil {
		return err
	}
	if len(req.LocalPaths) == 0 {
		return badRequest("localPaths 不能为空")
	}

	locals := make([]string, 0, len(req.LocalPaths))
	for _, p := range req.LocalPaths {
		resolved, e := s.resolveLocalPath(p)
		if e != nil {
			return e
		}
		locals = append(locals, resolved)
	}

	spec := &JobSpec{
		Type:          JobUpload,
		DriveId:       driveId,
		PanDir:        req.PanDir,
		LocalPaths:    locals,
		Parallel:      req.Parallel,
		MaxRetry:      req.MaxRetry,
		BlockSize:     req.BlockSize,
		IsOverwrite:   req.IsOverwrite,
		NoRapidUpload: req.NoRapidUpload,
		ExcludeNames:  req.ExcludeNames,
	}
	job, err := s.transfer.SubmitUpload(u, spec)
	if err != nil {
		return err
	}
	return writeOKErr(w, job.Snapshot(false))
}

func (s *Server) handleJobPause(w http.ResponseWriter, r *http.Request) error {
	j, err := s.transfer.Get(r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := j.Pause(); err != nil {
		return err
	}
	return writeOKErr(w, j.Snapshot(false))
}

func (s *Server) handleJobResume(w http.ResponseWriter, r *http.Request) error {
	j, err := s.transfer.Get(r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := j.Resume(); err != nil {
		return err
	}
	return writeOKErr(w, j.Snapshot(false))
}

func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) error {
	j, err := s.transfer.Get(r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := j.Cancel(); err != nil {
		return err
	}
	return writeOKErr(w, j.Snapshot(false))
}

func (s *Server) handleJobRetry(w http.ResponseWriter, r *http.Request) error {
	u, err := activeUser()
	if err != nil {
		return err
	}
	j, err := s.transfer.Retry(u, r.PathValue("id"))
	if err != nil {
		return err
	}
	return writeOKErr(w, j.Snapshot(false))
}

func (s *Server) handleJobDelete(w http.ResponseWriter, r *http.Request) error {
	if err := s.transfer.Remove(r.PathValue("id")); err != nil {
		return err
	}
	return writeOKErr(w, map[string]string{"id": r.PathValue("id")})
}

func (s *Server) handleTransferClear(w http.ResponseWriter, r *http.Request) error {
	return writeOKErr(w, map[string]int{"removed": s.transfer.Clear()})
}
