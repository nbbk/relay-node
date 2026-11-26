package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// ---------- 远程配置（v1.4.29，客户端 0.1.3 拉取用） ----------
// 客户端从面板拉远程配置，地址链第一位就是
// https://<panel_api_base>/client-config.json（panel_api_base 带端口）。
//
// 信任模型：信封必须含 signature 字段，客户端用编译进的 Ed25519 公钥
// 验签，签不过/缺签名当作没拉到；只接受 config_version 比缓存新的。
// 面板没有签名私钥、也不能现场签名，所以面板不生成内容、不改内容、
// 不验签，只原样 served 管理员已签发好的信封（字符串原样存在
// config.json 的 client_config_json 字段里，见 config.go）。
// 发布时只做形状校验（合法 JSON 对象＋非空 signature 字符串＋
// config_version 数字＋panel 对象），保证发出去的东西客户端能解析、
// 能验签；签名本身是假是真面板不管（测试里用假字符串即可）。
//
// 清空口子（二选一，只留这一个）：POST 同一接口、config 传空字符串
// （或只含空白）即撤下，不另设 DELETE 接口。

// clientConfigReq 发布/撤下远程配置的请求体（管理端，requireAuth）。
// 字段名定为 config（与前端 saveClientConfig/clearClientConfig 的
// 契约一致，见 test-1431-client-config.js 的 mock），即
// {config: "<信封原文>"}；不是 config.json 里的 client_config_json。
// 未知字段 400（decodeJSON 全局 DisallowUnknownFields）。
type clientConfigReq struct {
	Config string `json:"config"`
}

// validateClientConfigEnvelope 校验已签名信封原文的形状：合法 JSON、
// 是对象、含非空 signature 字符串、含 config_version 数字、含 panel
// 对象。返回（config_version，错误文案），错误文案空串 = 通过。
// 注意：这里不验签——面板没有验签用的公钥/私钥，验签是客户端的事。
func validateClientConfigEnvelope(raw string) (float64, string) {
	var env map[string]any
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return 0, "信封不是合法 JSON"
	}
	sig, ok := env["signature"].(string)
	if !ok || strings.TrimSpace(sig) == "" {
		return 0, "信封缺少 signature"
	}
	ver, ok := env["config_version"].(float64)
	if !ok {
		return 0, "信封缺少 config_version"
	}
	if _, ok := env["panel"].(map[string]any); !ok {
		return 0, "信封缺少 panel"
	}
	return ver, ""
}

// handleClientConfigGet GET /client-config.json：公开拉取远程配置
// （免鉴权，只读）。已发布时 200 原样返回存的字节（Content-Type:
// application/json，不重编码、不改字节）；未发布时 404（客户端把
// 404 认作“配置还没发布”，不要回 200 空）。
func (s *Server) handleClientConfigGet(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(s.cfg.ClientConfigJSON) == "" {
		writeJSON(w, 404, errResp("远程配置还没发布"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(s.cfg.ClientConfigJSON))
}

// handleClientConfigSet POST /api/v1/settings/client-config：发布或
// 撤下远程配置（requireAuth）。请求体 {config: "<信封原文>"}；
// 形状不对 400；合法则存入 config 并落盘，回包带 config_version；
// 传空字符串即撤下（回包不带 config_version）。审计记“发布远程配置/
// 撤下远程配置”，detail 只记 config_version，绝不记信封内容。
func (s *Server) handleClientConfigSet(w http.ResponseWriter, r *http.Request) {
	var req clientConfigReq
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if strings.TrimSpace(req.Config) == "" {
		s.cfg.ClientConfigJSON = ""
		if err := s.saveConfig(); err != nil {
			writeJSON(w, 500, errResp("保存失败："+err.Error()))
			return
		}
		s.audit(s.currentUser(r), "撤下远程配置", "", "成功", "")
		writeJSON(w, 200, okResp(nil))
		return
	}
	ver, msg := validateClientConfigEnvelope(req.Config)
	if msg != "" {
		writeJSON(w, 400, errResp(msg))
		return
	}
	// 原文原样存（不 Trim、不重编码）：GET 要原样返回存的字节。
	s.cfg.ClientConfigJSON = req.Config
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "发布远程配置", "", "成功", "config_version="+strconv.FormatFloat(ver, 'f', -1, 64))
	writeJSON(w, 200, okResp(map[string]any{"config_version": ver}))
}
