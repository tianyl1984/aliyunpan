package webui

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/tickstep/aliyunpan/internal/config"
	"github.com/tickstep/aliyunpan/library/homedir"
)

// defaultLocalRoots 未显式配置 --local-root 时允许浏览的服务器本地根目录
func defaultLocalRoots() []string {
	roots := []string{}
	if home, err := homedir.Dir(); err == nil && home != "" {
		roots = append(roots, home)
	}
	if d := config.GetDefaultDownloadDir(); d != "" {
		roots = append(roots, d)
	}
	if sd := config.Config.SaveDir; sd != "" {
		roots = append(roots, sd)
	}
	return normalizeRoots(roots)
}

// normalizeRoots 把根目录绝对化、解符号链接、去重
func normalizeRoots(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, r := range in {
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	sort.Strings(out)
	return out
}

// resolveLocalPath 校验并解析一个服务器本地路径。
// 校验链：Clean -> Abs -> EvalSymlinks -> 前缀比对，防止 .. 与符号链接逃逸出根目录白名单。
func (s *Server) resolveLocalPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", badRequest("path 不能为空")
	}
	abs, err := filepath.Abs(filepath.Clean(p))
	if err != nil {
		return "", badRequest("无效路径: " + p)
	}

	// 逐级向上找到一个已存在的祖先来解符号链接：目标本身可能还不存在（比如新建下载目录）
	probe := abs
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	resolvedProbe, err := filepath.EvalSymlinks(probe)
	if err != nil {
		resolvedProbe = probe
	}
	// 把未存在的尾部再接回去
	tail := strings.TrimPrefix(abs, probe)
	resolved := filepath.Join(resolvedProbe, tail)

	for _, root := range s.localRoots {
		if pathWithinRoot(resolved, root) {
			return resolved, nil
		}
	}
	return "", forbidden("路径不在允许访问的目录白名单内: " + abs)
}

func pathWithinRoot(p, root string) bool {
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
		root = strings.ToLower(root)
	}
	if p == root {
		return true
	}
	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}
	return strings.HasPrefix(p, root)
}

type localEntryDTO struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	IsDir    bool   `json:"isDir"`
	ModTime  string `json:"modTime"`
	Readable bool   `json:"readable"`
}

func (s *Server) handleLocalRoots(w http.ResponseWriter, r *http.Request) error {
	roots := make([]*localEntryDTO, 0, len(s.localRoots))
	for _, p := range s.localRoots {
		e := &localEntryDTO{Name: p, Path: p, IsDir: true, Readable: true}
		if fi, err := os.Stat(p); err == nil {
			e.ModTime = fi.ModTime().Format("2006-01-02 15:04:05")
		} else {
			e.Readable = false
		}
		roots = append(roots, e)
	}
	return writeOKErr(w, map[string]interface{}{
		"roots":     roots,
		"separator": string(filepath.Separator),
	})
}

func (s *Server) handleLocalList(w http.ResponseWriter, r *http.Request) error {
	target, err := s.resolveLocalPath(r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	fi, statErr := os.Stat(target)
	if statErr != nil {
		return notFound("路径不存在: " + target)
	}
	if !fi.IsDir() {
		return badRequest("不是目录: " + target)
	}

	entries, readErr := os.ReadDir(target)
	if readErr != nil {
		return forbidden("无法读取目录: " + readErr.Error())
	}

	showHidden := r.URL.Query().Get("hidden") == "1"
	dirsOnly := r.URL.Query().Get("dirsOnly") == "1"

	out := make([]*localEntryDTO, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		if dirsOnly && !e.IsDir() {
			continue
		}
		item := &localEntryDTO{
			Name:     name,
			Path:     filepath.Join(target, name),
			IsDir:    e.IsDir(),
			Readable: true,
		}
		if info, err := e.Info(); err == nil {
			item.Size = info.Size()
			item.ModTime = info.ModTime().Format("2006-01-02 15:04:05")
		}
		out = append(out, item)
	}
	// 目录在前，同类按名称排序
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})

	parent := filepath.Dir(target)
	if _, err := s.resolveLocalPath(parent); err != nil || parent == target {
		parent = ""
	}

	return writeOKErr(w, map[string]interface{}{
		"path":    target,
		"parent":  parent,
		"entries": out,
	})
}
