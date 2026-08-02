package webui

import (
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/tickstep/aliyunpan-api/aliyunpan"
	"github.com/tickstep/aliyunpan/internal/config"
)

const defaultListLimit = 200

// ---- 列表 / 详情 ----

type listResponse struct {
	Target     *fileDTO   `json:"target"`
	Files      []*fileDTO `json:"files"`
	NextMarker string     `json:"nextMarker"`
}

func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) error {
	u, err := activeUser()
	if err != nil {
		return err
	}
	q := r.URL.Query()
	driveId, err := resolveDriveId(u, q.Get("driveId"))
	if err != nil {
		return err
	}
	absPath := cleanPanPath(q.Get("path"))

	target, err := lookupFile(u, driveId, absPath)
	if err != nil {
		return err
	}
	if !target.IsFolder() {
		// 直接请求了一个文件，返回它自身
		return writeOKErr(w, &listResponse{Target: toFileDTO(target, ""), Files: []*fileDTO{}})
	}

	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = defaultListLimit
	}
	param := &aliyunpan.FileListParam{
		DriveId:        driveId,
		ParentFileId:   target.FileId,
		Limit:          limit,
		Marker:         q.Get("marker"),
		OrderBy:        parseOrderBy(q.Get("orderBy")),
		OrderDirection: parseOrderDirection(q.Get("order")),
	}

	result, apiErr := u.PanClient().OpenapiPanClient().FileList(param)
	if apiErr != nil {
		return upstreamError(apiErr)
	}
	return writeOKErr(w, &listResponse{
		Target:     toFileDTO(target, ""),
		Files:      toFileDTOList(result.FileList, target.Path),
		NextMarker: result.NextMarker,
	})
}

func parseOrderBy(v string) aliyunpan.FileOrderBy {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "name":
		return aliyunpan.FileOrderByName
	case "size":
		return aliyunpan.FileOrderBySize
	case "updated_at", "time", "updatedat":
		return aliyunpan.FileOrderByUpdatedAt
	case "created_at", "createdat":
		return aliyunpan.FileOrderByCreatedAt
	}
	return aliyunpan.FileOrderByName
}

func parseOrderDirection(v string) aliyunpan.FileOrderDirection {
	if strings.EqualFold(strings.TrimSpace(v), "desc") {
		return aliyunpan.FileOrderDirectionDesc
	}
	return aliyunpan.FileOrderDirectionAsc
}

func (s *Server) handleFileInfo(w http.ResponseWriter, r *http.Request) error {
	u, err := activeUser()
	if err != nil {
		return err
	}
	q := r.URL.Query()
	driveId, err := resolveDriveId(u, q.Get("driveId"))
	if err != nil {
		return err
	}
	if fileId := strings.TrimSpace(q.Get("fileId")); fileId != "" {
		fe, apiErr := u.PanClient().OpenapiPanClient().FileInfoById(driveId, fileId)
		if apiErr != nil {
			return upstreamError(apiErr)
		}
		return writeOKErr(w, toFileDTO(fe, ""))
	}
	fe, err := lookupFile(u, driveId, q.Get("path"))
	if err != nil {
		return err
	}
	return writeOKErr(w, toFileDTO(fe, ""))
}

// handleFileSearch 递归遍历目录做关键字匹配。
// 阿里云盘 OpenAPI 没有提供搜索接口，这里用递归列目录实现，因此限定了深度和结果数量。
func (s *Server) handleFileSearch(w http.ResponseWriter, r *http.Request) error {
	u, err := activeUser()
	if err != nil {
		return err
	}
	q := r.URL.Query()
	driveId, err := resolveDriveId(u, q.Get("driveId"))
	if err != nil {
		return err
	}
	keyword := strings.ToLower(strings.TrimSpace(q.Get("keyword")))
	if keyword == "" {
		return badRequest("keyword 不能为空")
	}
	root := cleanPanPath(q.Get("path"))
	maxDepth, _ := strconv.Atoi(q.Get("depth"))
	if maxDepth <= 0 || maxDepth > 8 {
		maxDepth = 4
	}
	maxResults, _ := strconv.Atoi(q.Get("limit"))
	if maxResults <= 0 || maxResults > 500 {
		maxResults = 200
	}

	var (
		results  []*fileDTO
		truncate bool
	)
	client := u.PanClient().OpenapiPanClient()
	// 递归列目录可能很慢，客户端断开或超时就及时收手
	ctx := r.Context()

	var walk func(dirPath, parentFileId string, depth int)
	walk = func(dirPath, parentFileId string, depth int) {
		if truncate || depth > maxDepth {
			return
		}
		select {
		case <-ctx.Done():
			truncate = true
			return
		default:
		}
		fl, apiErr := client.FileListGetAll(&aliyunpan.FileListParam{
			DriveId:      driveId,
			ParentFileId: parentFileId,
			Limit:        100,
		}, 0)
		if apiErr != nil {
			return
		}
		for _, f := range fl {
			if f == nil {
				continue
			}
			full := path.Join(dirPath, f.FileName)
			if strings.Contains(strings.ToLower(f.FileName), keyword) {
				if len(results) >= maxResults {
					truncate = true
					return
				}
				f.Path = full
				results = append(results, toFileDTO(f, dirPath))
			}
			if f.IsFolder() {
				walk(full, f.FileId, depth+1)
				if truncate {
					return
				}
			}
		}
	}

	target, err := lookupFile(u, driveId, root)
	if err != nil {
		return err
	}
	if !target.IsFolder() {
		return badRequest("搜索起点必须是目录")
	}
	walk(target.Path, target.FileId, 1)

	return writeOKErr(w, map[string]interface{}{
		"files":     results,
		"truncated": truncate,
	})
}

// ---- 写操作 ----

type mkdirRequest struct {
	DriveId string `json:"driveId"`
	Path    string `json:"path"`
}

func (s *Server) handleFileMkdir(w http.ResponseWriter, r *http.Request) error {
	var req mkdirRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	u, err := activeUser()
	if err != nil {
		return err
	}
	driveId, err := resolveDriveId(u, req.DriveId)
	if err != nil {
		return err
	}
	full := cleanPanPath(req.Path)
	if full == "/" {
		return badRequest("不能创建根目录")
	}
	rs, apiErr := u.PanClient().OpenapiPanClient().MkdirByFullPath(driveId, full)
	if apiErr != nil {
		return upstreamError(apiErr)
	}
	if rs == nil || rs.FileId == "" {
		return internalError("创建文件夹失败: " + full)
	}
	u.DeleteCache(dirsOfPath(full))
	return writeOKErr(w, map[string]string{"fileId": rs.FileId, "path": full})
}

type pathsRequest struct {
	DriveId string   `json:"driveId"`
	Paths   []string `json:"paths"`
}

type itemResult struct {
	Path    string `json:"path"`
	FileId  string `json:"fileId,omitempty"`
	Success bool   `json:"success"`
	Reason  string `json:"reason,omitempty"`
}

func (s *Server) handleFileDelete(w http.ResponseWriter, r *http.Request) error {
	var req pathsRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if len(req.Paths) == 0 {
		return badRequest("paths 不能为空")
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	u, err := activeUser()
	if err != nil {
		return err
	}
	driveId, err := resolveDriveId(u, req.DriveId)
	if err != nil {
		return err
	}

	client := u.PanClient().OpenapiPanClient()
	results := make([]*itemResult, 0, len(req.Paths))
	cacheDirs := map[string]bool{}

	for _, p := range req.Paths {
		full := cleanPanPath(p)
		if full == "/" {
			results = append(results, &itemResult{Path: full, Reason: "不能删除根目录"})
			continue
		}
		fe, e := lookupFile(u, driveId, full)
		if e != nil {
			results = append(results, &itemResult{Path: full, Reason: e.Error()})
			continue
		}
		_, apiErr := client.FileDelete(&aliyunpan.FileBatchActionParam{DriveId: driveId, FileId: fe.FileId})
		if apiErr != nil {
			results = append(results, &itemResult{Path: full, FileId: fe.FileId, Reason: apiErr.Error()})
			continue
		}
		results = append(results, &itemResult{Path: full, FileId: fe.FileId, Success: true})
		cacheDirs[path.Dir(full)] = true
	}
	flushCache(u, cacheDirs)
	return writeOKErr(w, map[string]interface{}{"items": results})
}

type transformRequest struct {
	DriveId  string   `json:"driveId"`
	SrcPaths []string `json:"srcPaths"`
	DstPath  string   `json:"dstPath"`
}

func (s *Server) handleFileCopy(w http.ResponseWriter, r *http.Request) error {
	return s.fileTransform(w, r, false)
}

func (s *Server) handleFileMove(w http.ResponseWriter, r *http.Request) error {
	return s.fileTransform(w, r, true)
}

func (s *Server) fileTransform(w http.ResponseWriter, r *http.Request, isMove bool) error {
	var req transformRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if len(req.SrcPaths) == 0 {
		return badRequest("srcPaths 不能为空")
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	u, err := activeUser()
	if err != nil {
		return err
	}
	driveId, err := resolveDriveId(u, req.DriveId)
	if err != nil {
		return err
	}
	dst := cleanPanPath(req.DstPath)
	dstFe, err := lookupFile(u, driveId, dst)
	if err != nil {
		return err
	}
	if !dstFe.IsFolder() {
		return badRequest("目标路径必须是目录: " + dst)
	}

	client := u.PanClient().OpenapiPanClient()
	results := make([]*itemResult, 0, len(req.SrcPaths))
	cacheDirs := map[string]bool{dst: true}

	for _, p := range req.SrcPaths {
		full := cleanPanPath(p)
		if full == "/" {
			results = append(results, &itemResult{Path: full, Reason: "不能操作根目录"})
			continue
		}
		fe, e := lookupFile(u, driveId, full)
		if e != nil {
			results = append(results, &itemResult{Path: full, Reason: e.Error()})
			continue
		}
		var apiErrMsg string
		if isMove {
			_, apiErr := client.FileMove(&aliyunpan.FileMoveParam{
				DriveId:        driveId,
				FileId:         fe.FileId,
				ToDriveId:      driveId,
				ToParentFileId: dstFe.FileId,
			})
			if apiErr != nil {
				apiErrMsg = apiErr.Error()
			}
		} else {
			_, apiErr := client.FileCopy(&aliyunpan.FileCopyParam{
				DriveId:        driveId,
				FileId:         fe.FileId,
				ToParentFileId: dstFe.FileId,
			})
			if apiErr != nil {
				apiErrMsg = apiErr.Error()
			}
		}
		if apiErrMsg != "" {
			results = append(results, &itemResult{Path: full, FileId: fe.FileId, Reason: apiErrMsg})
			continue
		}
		results = append(results, &itemResult{Path: full, FileId: fe.FileId, Success: true})
		cacheDirs[path.Dir(full)] = true
	}
	flushCache(u, cacheDirs)
	return writeOKErr(w, map[string]interface{}{"items": results})
}

type renameRequest struct {
	DriveId string `json:"driveId"`
	Path    string `json:"path"`
	NewName string `json:"newName"`
}

func (s *Server) handleFileRename(w http.ResponseWriter, r *http.Request) error {
	var req renameRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	newName := strings.TrimSpace(req.NewName)
	if newName == "" {
		return badRequest("newName 不能为空")
	}
	if strings.ContainsAny(newName, "/\\") {
		return badRequest("文件名不能包含路径分隔符")
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	u, err := activeUser()
	if err != nil {
		return err
	}
	driveId, err := resolveDriveId(u, req.DriveId)
	if err != nil {
		return err
	}
	full := cleanPanPath(req.Path)
	if full == "/" {
		return badRequest("不能重命名根目录")
	}
	fe, err := lookupFile(u, driveId, full)
	if err != nil {
		return err
	}
	ok, apiErr := u.PanClient().OpenapiPanClient().FileRename(driveId, fe.FileId, newName)
	if apiErr != nil {
		return upstreamError(apiErr)
	}
	if !ok {
		return internalError("重命名失败: " + full)
	}
	flushCache(u, map[string]bool{path.Dir(full): true})
	return writeOKErr(w, map[string]string{
		"fileId": fe.FileId,
		"path":   path.Join(path.Dir(full), newName),
	})
}

// flushCache 清理受影响目录的列表缓存，让下一次 ls 拿到最新结果
func flushCache(u *config.PanUser, dirs map[string]bool) {
	if len(dirs) == 0 {
		return
	}
	list := make([]string, 0, len(dirs))
	for d := range dirs {
		list = append(list, d)
	}
	sort.Strings(list)
	u.DeleteCache(list)
}

// writeOKErr 是 writeOK 的 error 返回版本，便于 handler 直接 return
func writeOKErr(w http.ResponseWriter, data interface{}) error {
	writeOK(w, data)
	return nil
}
