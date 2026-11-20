package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ---------- 面板 Logo（v1.3.20） ----------
//
// 文件存在 PANEL_HOME/logo（更新不丢，面板安装/更新只换二进制与前端文件，
// 不动 PANEL_HOME）；MIME 只记在 config.json 的 logo_mime 里。
// 显示位置：面板左上角（侧边栏品牌旁）与登录页面板名称上方，都是圆形。
// GET /logo 是公开端点（登录页未登录也要显示），不走会话认证；
// 上传/删除走会话认证。备份 JSON 不含 Logo 文件（和密码哈希、令牌一样），
// 整机迁移后须重新上传一次。

// logo 文件名与大小上限：2MB，只接受图片。
const logoMaxBytes = 2 << 20

func (s *Server) logoPath() string {
	return filepath.Join(s.env.PanelHome, "logo")
}

func (s *Server) logoExists() bool {
	if s.cfg.LogoMime == "" {
		return false
	}
	st, err := os.Stat(s.logoPath())
	return err == nil && st.Size() > 0
}

// handleLogo 公开读取 Logo：有就返回文件（Content-Type 按上传时嗅探到的
// MIME），没有就 404（前端显示默认样式）。Cache-Control 用 no-cache，
// 换了 Logo 后立即生效，不用等缓存过期。
func (s *Server) handleLogo(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.logoPath())
	if err != nil || len(data) == 0 {
		http.NotFound(w, r)
		return
	}
	mime := s.cfg.LogoMime
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mime, "image/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// handleLogoUpload 上传 Logo（会话认证）：multipart 表单，字段名 logo。
// 只接受 PNG/JPEG/GIF/WebP（按文件头嗅探，不信扩展名与客户端传的 MIME），
// 超过 2MB 拒绝。保存到 PANEL_HOME/logo（600 权限），MIME 记进 config.json。
func (s *Server) handleLogoUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, logoMaxBytes+1024*1024)
	if err := r.ParseMultipartForm(logoMaxBytes + 1024*1024); err != nil {
		writeJSON(w, 400, errResp("上传失败：文件太大（上限 2MB）或格式不对"))
		return
	}
	f, _, err := r.FormFile("logo")
	if err != nil {
		writeJSON(w, 400, errResp("请选择一张图片上传"))
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, logoMaxBytes+1))
	if err != nil {
		writeJSON(w, 400, errResp("读取图片失败"))
		return
	}
	if len(data) > logoMaxBytes {
		writeJSON(w, 400, errResp("图片太大（上限 2MB），请压缩后再传"))
		return
	}
	mime := http.DetectContentType(data)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		writeJSON(w, 400, errResp("只支持 PNG、JPG、GIF、WebP 图片"))
		return
	}
	oldMime := s.cfg.LogoMime
	if err := writeFileAtomic(s.logoPath(), data, 0o600); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.cfg.LogoMime = mime
	if err := s.saveConfig(); err != nil {
		s.cfg.LogoMime = oldMime
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "上传面板 Logo", "", "成功", "文件已存好，更新不丢")
	writeJSON(w, 200, okResp(map[string]any{"has_logo": true, "mime": mime}))
}

// handleLogoDelete 删除 Logo（会话认证）：删文件、清 MIME，之后显示默认样式。
func (s *Server) handleLogoDelete(w http.ResponseWriter, r *http.Request) {
	if !s.logoExists() {
		writeJSON(w, 400, errResp("本来就没有 Logo"))
		return
	}
	_ = os.Remove(s.logoPath())
	oldMime := s.cfg.LogoMime
	s.cfg.LogoMime = ""
	if err := s.saveConfig(); err != nil {
		s.cfg.LogoMime = oldMime
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "删除面板 Logo", "", "成功", "已恢复默认样式")
	writeJSON(w, 200, okResp(map[string]any{"has_logo": false}))
}
