package webui

import (
	"net/http"
	"runtime"
	"strconv"
	"strings"

	"github.com/tickstep/aliyunpan/internal/config"
	"github.com/tickstep/aliyunpan/internal/global"
)

// configDTO 暴露给前端的配置项。
// 严格白名单：openapiToken / webapiToken / clientSecret 等敏感字段永不返回。
type configDTO struct {
	CacheSize           int    `json:"cacheSize"`
	MaxDownloadParallel int    `json:"maxDownloadParallel"`
	MaxUploadParallel   int    `json:"maxUploadParallel"`
	MaxDownloadRate     int64  `json:"maxDownloadRate"`
	MaxUploadRate       int64  `json:"maxUploadRate"`
	SaveDir             string `json:"saveDir"`
	Proxy               string `json:"proxy"`
	LocalAddrs          string `json:"localAddrs"`
	PreferIPType        string `json:"preferIPType"`
	VideoFileExtensions string `json:"videoFileExtensions"`
	FileRecordConfig    string `json:"fileRecordConfig"`
	DeviceName          string `json:"deviceName"`
}

// configPatch 可更新的字段。全部用指针，nil 表示本次不修改。
type configPatch struct {
	CacheSize           *int    `json:"cacheSize"`
	MaxDownloadParallel *int    `json:"maxDownloadParallel"`
	MaxUploadParallel   *int    `json:"maxUploadParallel"`
	MaxDownloadRate     *int64  `json:"maxDownloadRate"`
	MaxUploadRate       *int64  `json:"maxUploadRate"`
	SaveDir             *string `json:"saveDir"`
	Proxy               *string `json:"proxy"`
	LocalAddrs          *string `json:"localAddrs"`
	PreferIPType        *string `json:"preferIPType"`
	VideoFileExtensions *string `json:"videoFileExtensions"`
	FileRecordConfig    *string `json:"fileRecordConfig"`
	DeviceName          *string `json:"deviceName"`
}

func currentConfigDTO() *configDTO {
	c := config.Config
	return &configDTO{
		CacheSize:           c.CacheSize,
		MaxDownloadParallel: c.MaxDownloadParallel,
		MaxUploadParallel:   c.MaxUploadParallel,
		MaxDownloadRate:     c.MaxDownloadRate,
		MaxUploadRate:       c.MaxUploadRate,
		SaveDir:             c.SaveDir,
		Proxy:               c.Proxy,
		LocalAddrs:          c.LocalAddrs,
		PreferIPType:        c.PreferIPType,
		VideoFileExtensions: c.VideoFileExtensions,
		FileRecordConfig:    c.FileRecordConfig,
		DeviceName:          c.DeviceName,
	}
}

func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) error {
	return writeOKErr(w, map[string]interface{}{
		"config": currentConfigDTO(),
		"limits": map[string]interface{}{
			"maxFileDownloadParallelNum": config.MaxFileDownloadParallelNum,
			"maxFileUploadParallelNum":   config.MaxFileUploadParallelNum,
		},
	})
}

func (s *Server) handleConfigUpdate(w http.ResponseWriter, r *http.Request) error {
	var patch configPatch
	if err := decodeJSON(r, &patch); err != nil {
		return err
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	c := config.Config
	if patch.CacheSize != nil {
		if *patch.CacheSize < 0 {
			return badRequest("cacheSize 不能为负数")
		}
		c.CacheSize = *patch.CacheSize
	}
	if patch.MaxDownloadParallel != nil {
		v := *patch.MaxDownloadParallel
		if v < 0 || v > config.MaxFileDownloadParallelNum {
			return badRequest("maxDownloadParallel 取值范围 0 ~ " + strconv.Itoa(config.MaxFileDownloadParallelNum))
		}
		c.MaxDownloadParallel = v
	}
	if patch.MaxUploadParallel != nil {
		v := *patch.MaxUploadParallel
		if v < 0 || v > config.MaxFileUploadParallelNum {
			return badRequest("maxUploadParallel 取值范围 0 ~ " + strconv.Itoa(config.MaxFileUploadParallelNum))
		}
		c.MaxUploadParallel = v
	}
	if patch.MaxDownloadRate != nil {
		if *patch.MaxDownloadRate < 0 {
			return badRequest("maxDownloadRate 不能为负数")
		}
		c.MaxDownloadRate = *patch.MaxDownloadRate
	}
	if patch.MaxUploadRate != nil {
		if *patch.MaxUploadRate < 0 {
			return badRequest("maxUploadRate 不能为负数")
		}
		c.MaxUploadRate = *patch.MaxUploadRate
	}
	if patch.SaveDir != nil {
		c.SaveDir = strings.TrimSpace(*patch.SaveDir)
	}
	if patch.Proxy != nil {
		c.Proxy = strings.TrimSpace(*patch.Proxy)
	}
	if patch.LocalAddrs != nil {
		c.LocalAddrs = strings.TrimSpace(*patch.LocalAddrs)
	}
	if patch.PreferIPType != nil {
		v := strings.ToLower(strings.TrimSpace(*patch.PreferIPType))
		if v != "" && v != "ipv4" && v != "ipv6" {
			return badRequest("preferIPType 只能是 ipv4 或 ipv6")
		}
		c.PreferIPType = v
	}
	if patch.VideoFileExtensions != nil {
		c.VideoFileExtensions = strings.TrimSpace(*patch.VideoFileExtensions)
	}
	if patch.FileRecordConfig != nil {
		v := strings.TrimSpace(*patch.FileRecordConfig)
		if v != "1" && v != "2" {
			return badRequest("fileRecordConfig 只能是 1(开启) 或 2(关闭)")
		}
		c.FileRecordConfig = v
	}
	if patch.DeviceName != nil {
		if v := strings.TrimSpace(*patch.DeviceName); v != "" {
			c.DeviceName = v
		}
	}

	s.saveConfigLocked()
	return writeOKErr(w, map[string]interface{}{"config": currentConfigDTO()})
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) error {
	return writeOKErr(w, map[string]interface{}{
		"version":        global.AppVersion,
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"configDir":      config.GetConfigDir(),
		"logDir":         config.GetLogDir(),
		"defaultSaveDir": config.GetDefaultDownloadDir(),
		"listenAddr":     s.addr(),
		"allowShell":     s.opt.AllowShell,
		"tlsEnabled":     s.tlsEnabled(),
		"localRoots":     s.localRoots,
	})
}
