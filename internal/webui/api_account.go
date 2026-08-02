package webui

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tickstep/aliyunpan/internal/command"
	"github.com/tickstep/aliyunpan/internal/config"
	"github.com/tickstep/aliyunpan/internal/functions/panlogin"
	"github.com/tickstep/aliyunpan/internal/global"
)

type driveDTO struct {
	DriveId   string `json:"driveId"`
	DriveName string `json:"driveName"`
	DriveTag  string `json:"driveTag"`
	Active    bool   `json:"active"`
}

type accountDTO struct {
	UserId      string      `json:"userId"`
	Nickname    string      `json:"nickname"`
	AccountName string      `json:"accountName"`
	Active      bool        `json:"active"`
	ActiveDrive string      `json:"activeDriveId"`
	Drives      []*driveDTO `json:"drives"`
}

func toAccountDTO(u *config.PanUser, activeUID string) *accountDTO {
	d := &accountDTO{
		UserId:      u.UserId,
		Nickname:    u.Nickname,
		AccountName: u.AccountName,
		Active:      u.UserId == activeUID,
		ActiveDrive: u.ActiveDriveId,
		Drives:      make([]*driveDTO, 0, len(u.DriveList)),
	}
	for _, dr := range u.DriveList {
		d.Drives = append(d.Drives, &driveDTO{
			DriveId:   dr.DriveId,
			DriveName: dr.DriveName,
			DriveTag:  dr.DriveTag,
			Active:    dr.DriveId == u.ActiveDriveId,
		})
	}
	return d
}

func (s *Server) handleAccountCurrent(w http.ResponseWriter, r *http.Request) error {
	u := config.Config.ActiveUser()
	if u == nil || u.UserId == "" {
		return writeOKErr(w, map[string]interface{}{"loggedIn": false})
	}
	return writeOKErr(w, map[string]interface{}{
		"loggedIn": true,
		"account":  toAccountDTO(u, config.Config.ActiveUID),
	})
}

func (s *Server) handleAccountList(w http.ResponseWriter, r *http.Request) error {
	list := make([]*accountDTO, 0, len(config.Config.UserList))
	for _, u := range config.Config.UserList {
		list = append(list, toAccountDTO(u, config.Config.ActiveUID))
	}
	return writeOKErr(w, map[string]interface{}{"accounts": list})
}

func (s *Server) handleAccountDrives(w http.ResponseWriter, r *http.Request) error {
	u, err := activeUser()
	if err != nil {
		return err
	}
	return writeOKErr(w, map[string]interface{}{"drives": toAccountDTO(u, config.Config.ActiveUID).Drives})
}

func (s *Server) handleAccountQuota(w http.ResponseWriter, r *http.Request) error {
	u, err := activeUser()
	if err != nil {
		return err
	}
	info, apiErr := u.PanClient().OpenapiPanClient().GetUserInfo()
	if apiErr != nil {
		return upstreamError(apiErr)
	}
	var ratio float64
	if info.TotalSize > 0 {
		ratio = 100 * float64(info.UsedSize) / float64(info.TotalSize)
	}
	return writeOKErr(w, map[string]interface{}{
		"userId":              info.UserId,
		"nickname":            info.Nickname,
		"totalSize":           info.TotalSize,
		"usedSize":            info.UsedSize,
		"usedRatio":           ratio,
		"thirdPartyVip":       info.ThirdPartyVip,
		"thirdPartyVipExpire": info.ThirdPartyVipExpire,
	})
}

type switchAccountRequest struct {
	UserId string `json:"userId"`
}

func (s *Server) handleAccountSwitch(w http.ResponseWriter, r *http.Request) error {
	var req switchAccountRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.UserId) == "" {
		return badRequest("userId 不能为空")
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	u, err := config.Config.SwitchUser(req.UserId)
	if err != nil {
		return badRequest("切换账号失败: " + err.Error())
	}
	s.saveConfigLocked()
	s.events.Publish(Event{Type: EventAccountChanged, Data: map[string]interface{}{
		"activeUid": u.UserId, "nickname": u.Nickname, "driveId": u.ActiveDriveId,
	}})
	return writeOKErr(w, toAccountDTO(u, config.Config.ActiveUID))
}

type switchDriveRequest struct {
	DriveId string `json:"driveId"`
}

func (s *Server) handleDriveSwitch(w http.ResponseWriter, r *http.Request) error {
	var req switchDriveRequest
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
	u.ActiveDriveId = driveId
	s.saveConfigLocked()
	s.events.Publish(Event{Type: EventAccountChanged, Data: map[string]interface{}{
		"activeUid": u.UserId, "nickname": u.Nickname, "driveId": driveId,
	}})
	return writeOKErr(w, toAccountDTO(u, config.Config.ActiveUID))
}

func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request) error {
	userId := strings.TrimSpace(r.PathValue("userId"))
	if userId == "" {
		return badRequest("userId 不能为空")
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	deleted, err := config.Config.DeleteUser(userId)
	if err != nil {
		return badRequest("退出账号失败: " + err.Error())
	}
	s.saveConfigLocked()
	s.events.Publish(Event{Type: EventAccountChanged, Data: map[string]interface{}{
		"activeUid": config.Config.ActiveUID,
	}})
	return writeOKErr(w, map[string]string{"userId": deleted.UserId, "nickname": deleted.Nickname})
}

// ---- 扫码登录 ----
//
// CLI 的 login 命令流程是：打印授权 URL → 阻塞等用户按 Enter → 调 GetLoginToken。
// Web 端把「按 Enter」换成前端轮询 poll 接口，其余完全复用 panlogin 的能力。

func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) error {
	h := panlogin.NewLoginHelper(config.DefaultTokenServiceWebHost)
	qr, err := h.GetQRCodeLoginUrl("")
	if err != nil {
		return internalError("获取登录链接失败: " + err.Error())
	}
	if qr == nil || qr.TokenId == "" {
		return internalError("获取登录链接失败: 服务端未返回 ticketId")
	}
	ticketId := qr.TokenId

	redirectSuffix := "auth2"
	if global.IsSupportNoneOpenApiCommands {
		redirectSuffix = "auth"
	}
	authorizeUrl := fmt.Sprintf(
		"https://openapi.alipan.com/oauth/authorize?client_id=%s"+
			"&redirect_uri=https%%3A%%2F%%2Fapi.tickstep.com%%2Fauth%%2Ftickstep%%2Faliyunpan%%2Ftoken%%2Fopenapi%%2F%s%%2F%s"+
			"&scope=user:base,file:all:read,file:all:write,file:share:write,album:shared:read",
		config.Config.ClientId, ticketId, redirectSuffix)

	return writeOKErr(w, map[string]interface{}{
		"ticketId":     ticketId,
		"authorizeUrl": authorizeUrl,
		"expiresIn":    300,
		"tip":          "在新窗口完成授权和扫码两步登录后，本页会自动完成登录",
	})
}

func (s *Server) handleOAuthPoll(w http.ResponseWriter, r *http.Request) error {
	ticketId := strings.TrimSpace(r.URL.Query().Get("ticketId"))
	if ticketId == "" {
		return badRequest("ticketId 不能为空")
	}

	h := panlogin.NewLoginHelper(config.DefaultTokenServiceWebHost)
	comToken, err := h.GetLoginToken(ticketId)
	if err != nil || comToken == nil || comToken.Openapi == nil || comToken.Openapi.AccessToken == "" {
		// 还没授权完成，前端继续轮询
		return writeOKErr(w, map[string]interface{}{"status": "pending"})
	}

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	openToken := &config.PanClientToken{
		AccessToken: comToken.Openapi.AccessToken,
		Expired:     comToken.Openapi.Expired,
	}
	webToken := &config.PanClientToken{}
	if comToken.Webapi != nil {
		webToken.AccessToken = comToken.Webapi.AccessToken
		webToken.Expired = comToken.Webapi.Expired
	}
	cloudUser, e := config.SetupUserByCookie(openToken, webToken,
		ticketId, "",
		config.Config.DeviceId, config.Config.DeviceName,
		config.Config.ClientId, config.Config.ClientSecret)
	if cloudUser == nil {
		msg := "登录失败"
		if e != nil {
			msg += ": " + e.Error()
		}
		return internalError(msg)
	}
	cloudUser.TicketId = ticketId
	config.Config.SetActiveUser(cloudUser)
	s.saveConfigLocked()

	s.events.Publish(Event{Type: EventAccountChanged, Data: map[string]interface{}{
		"activeUid": cloudUser.UserId, "nickname": cloudUser.Nickname, "driveId": cloudUser.ActiveDriveId,
	}})
	return writeOKErr(w, map[string]interface{}{
		"status":  "ok",
		"account": toAccountDTO(cloudUser, config.Config.ActiveUID),
	})
}

// saveConfigLocked 立即把配置落盘。
// 必须立即保存而不是延迟批量写：网页控制台里执行的每条 CLI 命令都会触发
// command.ReloadConfigFunc，延迟落盘会被 Reload 覆盖掉。
// 调用方必须已持有 s.cfgMu。
func (s *Server) saveConfigLocked() {
	if err := command.SaveConfigFunc(nil); err != nil {
		s.logf("保存配置失败: %v", err)
	}
}
