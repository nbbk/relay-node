package main

// ---------- 轻量节点后端（v1.4.2，切片 3）：用户同步 ----------
//
// 每节点用户指派＋轮换 UUID。指派存在 BackendNode.UserIDs 里
// （nil = 全部未停用用户，见 node_backend.go；非 nil = 只发列表里
// 的用户，空数组 = 一个不发）。新建节点默认 UserIDs=[]（一个不发，
// §5.3：老用户不自动上新节点，须在“发到节点”里手动勾；nil = 全部
// 只留给 1.4.2 之前建的老节点）。
//
// 语义（写死，文档与前端都按这一份来）：
//   - GET /api/v1/nodes/{id}/users 返回该节点有效用户（未停用、已指派）
//     与 all_users 标记（UserIDs 是不是 nil）及 user_ids（存的列表，
//     nil = 全部、[] = 一个不发）。
//   - PUT /api/v1/nodes/{id}/users 全量替换指派：请求 {user_ids: [...]}
//     里 user_ids 是 null 或字段缺失 = 全部用户；[] 空数组 = 一个不发；
//     非空数组 = 只发这些用户。不认的用户键直接 400。
//   - POST /api/v1/users/{user_id}/rotate-uuid：user_id 是用户键
//     "subuser:su-…" / "merged:ms-…"（不带 kind 的裸 id 也认，先找
//     subuser 再找 merged）；轮换该用户在所有已指派节点上的 per-node
//     UUID（NodeUUIDs 里按节点地址存的那些），每个节点地址各换一个
//     新 UUID；旧 UUID 在 agent 下次拉取应用后失效。
//
// 关键契约：任何用户集合变化（指派改、停用／恢复、轮换、删除）都必须
// bump 受影响节点的 ConfigVersion，否则 agent 60 秒内拉不到变化。
// 指派与轮换在本文件直接 bump；停用／恢复走 user_traffic.go 里的
// disableUserForwardsLocked／resumeUserForwardsLocked／
// clearUserDisabled 三个 choke 点（见 bumpBackendNodesForUserLocked
// 的调用）；删除在 subscription.go 的两个删除 handler 里 bump。

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// bumpBackendNodesForUserLocked 该用户已指派的所有节点 ConfigVersion＋1
// （停用／恢复／删除时调）。调用方须持有 opMu；本函数自己拿 nodes.mu
// （与其它 handler 同样的加锁顺序：opMu 在外、nodes.mu 在内），改完
// 自己 saveLocked 落盘。节点 UserIDs 为 nil = 该用户在全部节点上，
// 所以停用一个用户要 bump 全部节点。
func (s *Server) bumpBackendNodesForUserLocked(key string) {
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	changed := false
	for _, node := range s.nodes.data.Nodes {
		if nodeHasUser(node, key) {
			node.ConfigVersion++
			changed = true
		}
	}
	if changed {
		_ = s.nodes.saveLocked()
	}
}

// findBackendUser 按用户键找订阅记录：返回 kind、id、用户名、停用标志
// 与 NodeUUIDs 表（调用方改完要自己 saveConfig 落盘）。
func (s *Server) findBackendUser(key string) (kind, id, name string, disabled bool, uuids map[string]string, ok bool) {
	kind, id, found := strings.Cut(strings.TrimSpace(key), ":")
	if !found || (kind != "subuser" && kind != "merged") || id == "" {
		return "", "", "", false, nil, false
	}
	if kind == "subuser" {
		if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
			return kind, id, u.Name, u.Disabled, u.NodeUUIDs, true
		}
		return "", "", "", false, nil, false
	}
	if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil {
		return kind, id, ms.Name, ms.Disabled, ms.NodeUUIDs, true
	}
	return "", "", "", false, nil, false
}

// parseRotateUserKey 解析轮换接口的 user_id：带 kind 的键直接用；
// 不带 kind 的裸 id 先找 subuser 再找 merged；找不到返回 ok=false。
func (s *Server) parseRotateUserKey(raw string) (key string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if strings.Contains(raw, ":") {
		if _, _, _, _, _, found := s.findBackendUser(raw); found {
			return raw, true
		}
		return "", false
	}
	for _, kind := range []string{"subuser", "merged"} {
		k := userSubKey(kind, raw)
		if _, _, _, _, _, found := s.findBackendUser(k); found {
			return k, true
		}
	}
	return "", false
}

// handleNodeUsersList GET /api/v1/nodes/{id}/users：该节点有效用户列表
// （未停用、已指派）＋ all_users 标记与存的 user_ids。
func (s *Server) handleNodeUsersList(w http.ResponseWriter, r *http.Request) {
	id, ok := nodeIDFromPath(r)
	if !ok {
		writeJSON(w, 400, errResp("节点 id 不对"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	node := s.nodes.getNode(id)
	if node == nil {
		writeJSON(w, 404, errResp("节点不存在"))
		return
	}
	allUsers := node.UserIDs == nil
	userIDs := node.UserIDs
	users, changed := s.nodeBackendUsersForNode(node)
	if changed {
		_ = s.saveConfig()
	}
	rows := make([]map[string]any, 0, len(users))
	for _, u := range users {
		rows = append(rows, map[string]any{
			"user_id": u.UserID, "username": u.Name,
			"vless_uuid": u.VLESSUUID, "status": "active",
		})
	}
	writeJSON(w, 200, okResp(map[string]any{
		"node_id": id, "all_users": allUsers, "user_ids": userIDs, "users": rows,
	}))
}

// handleNodeUsersUpdate PUT /api/v1/nodes/{id}/users：全量替换该节点的
// 用户指派。user_ids 为 null 或缺失 = 全部用户；[] = 一个不发。
// 改完 bump ConfigVersion，agent 60 秒内拉到新用户列表。
func (s *Server) handleNodeUsersUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := nodeIDFromPath(r)
	if !ok {
		writeJSON(w, 400, errResp("节点 id 不对"))
		return
	}
	var req struct {
		UserIDs *[]string `json:"user_ids"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	node := s.nodes.getNode(id)
	if node == nil {
		writeJSON(w, 404, errResp("节点不存在"))
		return
	}
	var list []string // nil = 全部用户
	if req.UserIDs != nil {
		seen := map[string]bool{}
		for _, k := range *req.UserIDs {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if _, _, _, _, _, found := s.findBackendUser(k); !found {
				writeJSON(w, 400, errResp("没有这个用户："+k))
				return
			}
			if !seen[k] {
				seen[k] = true
				list = append(list, k)
			}
		}
		if list == nil {
			list = []string{} // 显式 []（或全是空串）= 一个不发，别掉回 nil
		}
	}
	node.UserIDs = list
	s.nodes.bumpNodeConfigVersionLocked(node)
	if err := s.nodes.saveLocked(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "改节点用户指派", fmt.Sprintf("节点 %d %s", node.ID, node.Name), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{
		"node_id": id, "all_users": node.UserIDs == nil, "user_ids": node.UserIDs,
		"user_count": len(list), "config_version": node.ConfigVersion,
	}))
}

// handleUserRotateNodeUUID POST /api/v1/users/{user_id}/rotate-uuid：
// 轮换该用户在所有已指派节点上的 per-node UUID（NodeUUIDs 里按节点
// 地址存的那些），每个节点地址各换一个新 UUID；受影响节点 bump
// ConfigVersion，agent 下次拉取（60 秒内）应用后旧 UUID 失效。
// 订阅渲染不受影响（订阅链接照常用各节点 UUID，下次取订阅即新 UUID）。
func (s *Server) handleUserRotateNodeUUID(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("user_id")
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	key, ok := s.parseRotateUserKey(raw)
	if !ok {
		writeJSON(w, 404, errResp("没有这个用户"))
		return
	}
	kind, id, name, _, _, _ := s.findBackendUser(key)
	// 先找受影响的节点（已指派该用户的节点；UserIDs 为 nil 的节点全算）。
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	var affected []*BackendNode
	for _, node := range s.nodes.data.Nodes {
		if nodeHasUser(node, key) {
			affected = append(affected, node)
		}
	}
	// 每个受影响节点地址各换一个新 UUID。
	uuids := map[string]string{}
	nodeIDs := make([]int64, 0, len(affected))
	for _, node := range affected {
		uuid := newNodeUUID()
		// 同一张表里不许撞（概率极低，撞了就重抽一个）。
		for {
			dup := false
			if kind == "subuser" {
				if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
					for _, v := range u.NodeUUIDs {
						if v == uuid {
							dup = true
							break
						}
					}
				}
			} else {
				if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil {
					for _, v := range ms.NodeUUIDs {
						if v == uuid {
							dup = true
							break
						}
					}
				}
			}
			if !dup {
				break
			}
			uuid = newNodeUUID()
		}
		if kind == "subuser" {
			if u := findSubUserByID(s.cfg.SubUsers, id); u != nil {
				if u.NodeUUIDs == nil {
					u.NodeUUIDs = map[string]string{}
				}
				u.NodeUUIDs[node.Address] = uuid
			}
		} else {
			if ms := findMergedSubByID(s.cfg.MergedSubs, id); ms != nil {
				if ms.NodeUUIDs == nil {
					ms.NodeUUIDs = map[string]string{}
				}
				ms.NodeUUIDs[node.Address] = uuid
			}
		}
		uuids[strconv.FormatInt(node.ID, 10)] = uuid
		nodeIDs = append(nodeIDs, node.ID)
		s.nodes.bumpNodeConfigVersionLocked(node)
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	if err := s.nodes.saveLocked(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	// vless_uuid：第一个受影响节点的 UUID（没有节点时为空串）。
	vlessUUID := ""
	if len(nodeIDs) > 0 {
		vlessUUID = uuids[strconv.FormatInt(nodeIDs[0], 10)]
	}
	note := "旧 UUID 在节点下次拉取（60 秒内）应用后失效，客户端须重新导入"
	if len(nodeIDs) == 0 {
		note = "该用户没有发到任何节点，无需轮换"
	}
	s.audit(s.currentUser(r), "轮换节点 UUID", name, "成功", fmt.Sprintf("用户 %s，%d 个节点", key, len(nodeIDs)))
	writeJSON(w, 200, okResp(map[string]any{
		"user_id": key, "username": name, "nodes": nodeIDs,
		"uuids": uuids, "vless_uuid": vlessUUID, "note": note,
	}))
}
