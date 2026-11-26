package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// ---------- 公告（v1.4.29，见 goals/goal-6/announcement-contract.md） ----------
// 面板侧多条公告：公开拉取（GET /api/v1/announcements 与 public-info
// 里的 announcements 字段，免鉴权）＋管理端新建/编辑/删除（requireAuth）。
// login_notice / login_signature 保持原样不动（登录页公告仍在）。
// 存储：config.json 的 announcements 字段；备份/恢复跟走（导出
// announcements 顶级字段，恢复时读回；与 sub_groups 同款口径）。

// Announcement 一条公告：id 服务端生成（an-＋6 位 hex），客户端只读；
// title/body 纯文本；pinned 置顶；时间 RFC3339 UTC。
type Announcement struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Pinned    bool   `json:"pinned"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// sortedAnnouncements 公告列表（已排序的副本，调用方随便改不影响 config）：
// 置顶在前（多条置顶按创建时间倒序），其余按 created_at 倒序（新的在前）。
// 空列表返回 []，不返回 null。
func (s *Server) sortedAnnouncements() []Announcement {
	out := make([]Announcement, 0, len(s.cfg.Announcements))
	out = append(out, s.cfg.Announcements...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		ti, ei := time.Parse(time.RFC3339, out[i].CreatedAt)
		tj, ej := time.Parse(time.RFC3339, out[j].CreatedAt)
		if ei == nil && ej == nil {
			return ti.After(tj)
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out
}

// findAnnouncement 按 ID 找公告：找不到返回 nil。
func (s *Server) findAnnouncement(id string) *Announcement {
	for i := range s.cfg.Announcements {
		if s.cfg.Announcements[i].ID == id {
			return &s.cfg.Announcements[i]
		}
	}
	return nil
}

// validAnnouncementText 校验一段公告文本（标题/内容共用）：纯文本，
// 控制字符只认换行/回车/制表（\n \r \t），其余拒绝——与登录页公告
// validLoginNotice 同款。返回错误文案，空串 = 通过。
func validAnnouncementText(s string, maxRunes int, what string) string {
	if utf8.RuneCountInString(s) > maxRunes {
		return what + "太长，最多 " + fmt.Sprintf("%d", maxRunes) + " 个字"
	}
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7f {
			return what + "不能含控制字符（换行/回车/制表除外）"
		}
	}
	return ""
}

// validAnnouncement 校验标题与内容（创建/编辑共用）：标题必填 1–60 字、
// 内容必填 1–2000 字。返回规范化后的标题、内容与错误文案。
func validAnnouncement(title, body string) (string, string, string) {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title == "" {
		return "", "", "请填公告标题"
	}
	if msg := validAnnouncementText(title, 60, "公告标题"); msg != "" {
		return "", "", msg
	}
	if body == "" {
		return "", "", "请填公告内容"
	}
	if msg := validAnnouncementText(body, 2000, "公告内容"); msg != "" {
		return "", "", msg
	}
	return title, body, ""
}

// announcementResp 一条公告的回包形状（与存储形状一致）。
func announcementResp(a Announcement) map[string]any {
	return map[string]any{
		"id": a.ID, "title": a.Title, "body": a.Body, "pinned": a.Pinned,
		"created_at": a.CreatedAt, "updated_at": a.UpdatedAt,
	}
}

// handleAnnouncementsList GET /api/v1/announcements：公开拉取公告列表
// （免鉴权，只读），按置顶＋创建时间倒序；空列表返回 []。
func (s *Server) handleAnnouncementsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, okResp(map[string]any{"announcements": s.sortedAnnouncements()}))
}

// announcementReq 新建/编辑公告的请求体（全量替换）：未知字段 400。
type announcementReq struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Pinned bool   `json:"pinned"`
}

// handleAnnouncementsCreate POST /api/v1/announcements：新建公告。
func (s *Server) handleAnnouncementsCreate(w http.ResponseWriter, r *http.Request) {
	var req announcementReq
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	title, body, msg := validAnnouncement(req.Title, req.Body)
	if msg != "" {
		writeJSON(w, 400, errResp(msg))
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	a := Announcement{ID: "an-" + randomTokenHex(3), Title: title, Body: body, Pinned: req.Pinned, CreatedAt: now, UpdatedAt: now}
	s.cfg.Announcements = append(s.cfg.Announcements, a)
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "新建公告", title, "成功", "")
	writeJSON(w, 200, okResp(announcementResp(a)))
}

// handleAnnouncementsUpdate PUT /api/v1/announcements/{id}：全量替换
// （标题/内容必填，pinned 跟着整份换）；不存在 404。
func (s *Server) handleAnnouncementsUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req announcementReq
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	title, body, msg := validAnnouncement(req.Title, req.Body)
	if msg != "" {
		writeJSON(w, 400, errResp(msg))
		return
	}
	a := s.findAnnouncement(id)
	if a == nil {
		writeJSON(w, 404, errResp("没有这条公告"))
		return
	}
	a.Title = title
	a.Body = body
	a.Pinned = req.Pinned
	a.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "编辑公告", title, "成功", "")
	writeJSON(w, 200, okResp(announcementResp(*a)))
}

// handleAnnouncementsDelete DELETE /api/v1/announcements/{id}：不存在 404。
func (s *Server) handleAnnouncementsDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a := s.findAnnouncement(id)
	if a == nil {
		writeJSON(w, 404, errResp("没有这条公告"))
		return
	}
	title := a.Title
	for i := range s.cfg.Announcements {
		if s.cfg.Announcements[i].ID == id {
			s.cfg.Announcements = append(s.cfg.Announcements[:i], s.cfg.Announcements[i+1:]...)
			break
		}
	}
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "删除公告", title, "成功", "")
	writeJSON(w, 200, okResp(nil))
}
