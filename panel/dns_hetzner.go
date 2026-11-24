package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ---------- Hetzner（v1.4.15） ----------
//
// API 依据：Hetzner Cloud API 官方文档与 OpenAPI（docs.hetzner.cloud）。
// 注意：旧 DNS API（dns.hetzner.com/api/v1）已于 2026 年 5 月下线，
// DNS 现走 Cloud API，RRSet 模型——不要按旧的 zone_id/value 字段写。
// 端点 https://api.hetzner.cloud/v1，认证 Authorization: Bearer <项目令牌>
// （旧 Auth-API-Token 已废）。zone 查 GET /v1/zones?name=<域名>；
// 创建 RRSet：POST /v1/zones/{zone}/rrsets
// {name:"_acme-challenge", type:"TXT", records:[{value:"<值>"}], ttl:60}；
// 已存在时改用 set_records 动作：
// POST /v1/zones/{zone}/rrsets/_acme-challenge/TXT/actions/set_records
// {records:[{value:"<值>"}]}；删除整条 RRSet：
// DELETE /v1/zones/{zone}/rrsets/_acme-challenge/TXT。
// 写操作都返回 201 + 一个异步 Action，须等 Action 成功才算写进 DNS。
// 限速 3600 请求/小时/项目，超限 429。

const hetznerAPI = "https://api.hetzner.cloud/v1"

// hetznerPollSleep 是等 Action 完成时的轮询间隔（包级变量只为测试能跳过等待）。
var hetznerPollSleep = time.Sleep

// hetznerHeaders 返回 Hetzner 的请求头（缺 Token 时报清楚去哪填）。
func hetznerHeaders(s *Server) (map[string]string, error) {
	token := s.cfg.HetznerToken
	if token == "" {
		return nil, fmt.Errorf("未配置 Hetzner 凭证（设置里填 Cloud 项目的 API Token，旧 DNS Console 的令牌在新 API 上不可用）")
	}
	return map[string]string{"Authorization": "Bearer " + token}, nil
}

// hetznerErr 把 Hetzner 的 JSON 错误拼成带 message 摘要的错误。
func hetznerErr(code int, b []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
		return fmt.Errorf("Hetzner 报错（HTTP %d，%s）：%s", code, e.Error.Code, truncateProviderBody([]byte(e.Error.Message)))
	}
	return fmt.Errorf("Hetzner 报错（HTTP %d）：%s", code, truncateProviderBody(b))
}

// hetznerWaitAction 等一个 Action 完成（最多轮询 30 次；已 success 直接回）。
func hetznerWaitAction(s *Server, actionID int64, headers map[string]string) error {
	for i := 0; i < 30; i++ {
		code, b, err := dnsHTTPGet(fmt.Sprintf("%s/actions/%d", hetznerAPI, actionID), headers)
		if err != nil {
			return fmt.Errorf("Hetzner 等操作完成失败：%v", err)
		}
		if code >= 300 {
			return hetznerErr(code, b)
		}
		var resp struct {
			Action struct {
				ID     int64  `json:"id"`
				Status string `json:"status"`
				Error  struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			} `json:"action"`
		}
		if json.Unmarshal(b, &resp) != nil {
			return fmt.Errorf("Hetzner 返回解析失败")
		}
		switch resp.Action.Status {
		case "success":
			return nil
		case "error":
			msg := resp.Action.Error.Message
			if msg == "" {
				msg = "操作失败"
			}
			return fmt.Errorf("Hetzner 操作失败（%s）：%s", resp.Action.Error.Code, truncateProviderBody([]byte(msg)))
		}
		hetznerPollSleep(time.Second)
	}
	return fmt.Errorf("Hetzner 操作 30 秒还没完成，请稍后重试")
}

// hetznerActionID 从写操作的响应里取 Action ID（没有就当已完成）。
func hetznerActionID(b []byte) int64 {
	var resp struct {
		Action struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
		} `json:"action"`
	}
	if json.Unmarshal(b, &resp) != nil {
		return 0
	}
	if resp.Action.Status == "success" {
		return 0
	}
	return resp.Action.ID
}

// hetznerFindZone 找域名所在的 zone：从完整域名逐级往上试，
// GET /v1/zones?name=<zone> 有结果的第一个就是。
func hetznerFindZone(s *Server, domain string) (zone string, headers map[string]string, err error) {
	headers, err = hetznerHeaders(s)
	if err != nil {
		return "", nil, err
	}
	parts := strings.Split(domain, ".")
	for i := 0; i < len(parts)-1; i++ {
		zone = strings.Join(parts[i:], ".")
		code, b, err := dnsHTTPGet(hetznerAPI+"/zones?name="+zone, headers)
		if err != nil {
			continue
		}
		if code >= 300 {
			return "", nil, hetznerErr(code, b)
		}
		var list struct {
			Zones []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"zones"`
		}
		if json.Unmarshal(b, &list) != nil {
			continue
		}
		for _, z := range list.Zones {
			if z.Name == zone {
				return zone, headers, nil
			}
		}
	}
	return "", nil, fmt.Errorf("Hetzner 里找不到域名 %s 的 Zone（请先在 Hetzner Console 添加该域名）", domain)
}

type hetznerRRSet struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	TTL     int    `json:"ttl"`
	Records []struct {
		Value string `json:"value"`
	} `json:"records"`
}

// hetznerGetRRSet 读 _acme-challenge 的 RRSet（不存在返回 nil, nil）。
func hetznerGetRRSet(s *Server, zone, relName string, headers map[string]string) (*hetznerRRSet, error) {
	code, b, err := dnsHTTPGet(hetznerAPI+"/zones/"+zone+"/rrsets?name="+relName+"&type=TXT", headers)
	if err != nil {
		return nil, fmt.Errorf("Hetzner 查 TXT 记录失败：%v", err)
	}
	if code == 404 {
		return nil, nil
	}
	if code >= 300 {
		return nil, hetznerErr(code, b)
	}
	var list struct {
		RRSets []hetznerRRSet `json:"rrsets"`
	}
	if json.Unmarshal(b, &list) != nil {
		return nil, fmt.Errorf("Hetzner 返回解析失败")
	}
	for _, r := range list.RRSets {
		if r.Name == relName && r.Type == "TXT" {
			return &r, nil
		}
	}
	return nil, nil
}

type hetznerProvider struct{}

func (hetznerProvider) ID() string { return "hetzner" }

func (hetznerProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	zone, headers, err := hetznerFindZone(s, domain)
	if err != nil {
		return nil, err
	}
	rr, err := hetznerGetRRSet(s, zone, dnsRelativeName(domain, zone), headers)
	if err != nil {
		return nil, err
	}
	if rr == nil {
		return nil, nil
	}
	var out []dnsTXTRecord
	for _, r := range rr.Records {
		out = append(out, dnsTXTRecord{Value: r.Value})
	}
	return out, nil
}

func (p hetznerProvider) Create(s *Server, domain, value string) (string, error) {
	zone, headers, err := hetznerFindZone(s, domain)
	if err != nil {
		return "", err
	}
	// RRSet 模型：同名 TXT 都在一条 RRSet 里。已存在就 set_records 精确
	// 换成新值（只动 _acme-challenge 这一条，别人的记录不动）；不存在就新建。
	relName := dnsRelativeName(domain, zone)
	existing, err := hetznerGetRRSet(s, zone, relName, headers)
	if err != nil {
		return "", err
	}
	payload, _ := json.Marshal(map[string]any{
		"records": []map[string]string{{"value": value}},
	})
	var code int
	var b []byte
	if existing != nil {
		code, b, err = dnsHTTPPost(hetznerAPI+"/zones/"+zone+"/rrsets/"+relName+"/TXT/actions/set_records", payload, "application/json", headers)
	} else {
		payload, _ = json.Marshal(map[string]any{
			"name": relName, "type": "TXT", "ttl": 60,
			"records": []map[string]string{{"value": value}},
		})
		code, b, err = dnsHTTPPost(hetznerAPI+"/zones/"+zone+"/rrsets", payload, "application/json", headers)
	}
	if err != nil {
		return "", fmt.Errorf("Hetzner 写 TXT 记录失败：%v。%s", err, providerManualHint("hetzner", domain))
	}
	if code != 201 {
		return "", fmt.Errorf("Hetzner 写 TXT 记录失败：%v。%s", hetznerErr(code, b), providerManualHint("hetzner", domain))
	}
	// 写操作是异步 Action：等它成功，TXT 才算真的写进 DNS。
	if id := hetznerActionID(b); id != 0 {
		if err := hetznerWaitAction(s, id, headers); err != nil {
			return "", fmt.Errorf("Hetzner 写 TXT 记录失败：%v。%s", err, providerManualHint("hetzner", domain))
		}
	}
	return "", nil // RRSet 模型没有单条记录 id，删时按名删整条 RRSet
}

func (hetznerProvider) Delete(s *Server, domain, id, value string) error {
	zone, headers, err := hetznerFindZone(s, domain)
	if err != nil {
		return err
	}
	code, b, err := dnsHTTPDelete(hetznerAPI+"/zones/"+zone+"/rrsets/"+dnsRelativeName(domain, zone)+"/TXT", headers)
	if err != nil {
		return err
	}
	if code == 404 {
		return nil // 已经没了
	}
	if code != 201 {
		return hetznerErr(code, b)
	}
	if aid := hetznerActionID(b); aid != 0 {
		return hetznerWaitAction(s, aid, headers)
	}
	return nil
}
