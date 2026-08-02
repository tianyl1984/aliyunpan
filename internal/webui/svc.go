package webui

import (
	"path"
	"strings"

	"github.com/tickstep/aliyunpan-api/aliyunpan"
	"github.com/tickstep/aliyunpan/internal/config"
)

// activeUser 获取当前登录的阿里云盘用户。
// 与 internal/command.GetActiveUser() 不同，这里做了 nil 检查，不会 panic。
func activeUser() (*config.PanUser, error) {
	u := config.Config.ActiveUser()
	if u == nil || u.UserId == "" {
		return nil, ErrNotLogin
	}
	if u.PanClient() == nil {
		return nil, ErrNotLogin
	}
	return u, nil
}

// resolveDriveId 解析前端传来的网盘标识。
// 支持三种形式：空字符串（当前活跃网盘）、标签（File/Resource/Album）、以及裸 driveId。
func resolveDriveId(u *config.PanUser, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		if u.ActiveDriveId == "" {
			return "", badRequest("当前账号没有可用的网盘")
		}
		return u.ActiveDriveId, nil
	}
	switch strings.ToLower(v) {
	case "file", "backup", "备份盘":
		if id := u.DriveList.GetFileDriveId(); id != "" {
			return id, nil
		}
		return "", notFound("当前账号没有备份盘")
	case "resource", "资源库":
		if id := u.DriveList.GetResourceDriveId(); id != "" {
			return id, nil
		}
		return "", notFound("当前账号没有资源库")
	case "album", "相册":
		if id := u.DriveList.GetAlbumDriveId(); id != "" {
			return id, nil
		}
		return "", notFound("当前账号没有相册")
	}
	// 裸 driveId，必须在该账号的网盘列表里
	for _, d := range u.DriveList {
		if d.DriveId == v {
			return v, nil
		}
	}
	return "", badRequest("无效的网盘ID: " + v)
}

// cleanPanPath 规范化网盘路径，保证是以 / 开头的绝对路径。
// Web 端是无状态的：所有路径都由前端给出绝对值，服务端不读写 PanUser.Workdir。
//
// 注意：网盘文件名允许首尾空格（例如 "xxx 4K "），所以这里不能对整个路径做 TrimSpace，
// 否则末级目录名的空格会被吃掉，get_by_path 会返回 NotFound.File。
func cleanPanPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 {
		p = path.Clean(p)
	}
	return p
}

// fileDTO 返回给前端的文件信息
type fileDTO struct {
	DriveId       string `json:"driveId"`
	FileId        string `json:"fileId"`
	FileName      string `json:"fileName"`
	FileSize      int64  `json:"fileSize"`
	FileType      string `json:"fileType"`
	IsFolder      bool   `json:"isFolder"`
	Path          string `json:"path"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
	FileExtension string `json:"fileExtension"`
	Category      string `json:"category"`
	ContentHash   string `json:"contentHash"`
	ParentFileId  string `json:"parentFileId"`
	Thumbnail     string `json:"thumbnail,omitempty"`
}

func toFileDTO(f *aliyunpan.FileEntity, parentPath string) *fileDTO {
	if f == nil {
		return nil
	}
	p := f.Path
	if p == "" && parentPath != "" {
		// FileList 接口不返回完整路径，这里按父目录拼出来
		p = path.Join(parentPath, f.FileName)
	}
	return &fileDTO{
		DriveId:       f.DriveId,
		FileId:        f.FileId,
		FileName:      f.FileName,
		FileSize:      f.FileSize,
		FileType:      f.FileType,
		IsFolder:      f.IsFolder(),
		Path:          p,
		CreatedAt:     f.CreatedAt,
		UpdatedAt:     f.UpdatedAt,
		FileExtension: f.FileExtension,
		Category:      f.Category,
		ContentHash:   f.ContentHash,
		ParentFileId:  f.ParentFileId,
		Thumbnail:     f.Thumbnail,
	}
}

func toFileDTOList(fl aliyunpan.FileList, parentPath string) []*fileDTO {
	out := make([]*fileDTO, 0, len(fl))
	for _, f := range fl {
		if f == nil {
			continue
		}
		out = append(out, toFileDTO(f, parentPath))
	}
	return out
}

// lookupFile 按路径取文件详情，并保证 Path 字段有值
func lookupFile(u *config.PanUser, driveId, absPath string) (*aliyunpan.FileEntity, error) {
	absPath = cleanPanPath(absPath)
	fe, apiErr := u.PanClient().OpenapiPanClient().FileInfoByPath(driveId, absPath)
	if apiErr != nil {
		return nil, upstreamError(apiErr)
	}
	if fe == nil {
		return nil, notFound("路径不存在: " + absPath)
	}
	if fe.Path == "" {
		fe.Path = absPath
	}
	if fe.DriveId == "" {
		fe.DriveId = driveId
	}
	return fe, nil
}

// dirsOfPath 返回一个路径涉及的所有父目录，用于清理目录列表缓存
func dirsOfPath(p string) []string {
	p = cleanPanPath(p)
	dirs := []string{"/"}
	cur := "/"
	for _, seg := range strings.Split(strings.Trim(p, "/"), "/") {
		if seg == "" {
			continue
		}
		cur = path.Join(cur, seg)
		dirs = append(dirs, cur)
	}
	return dirs
}
