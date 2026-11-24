package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---------- Spaceship（v1.4.15，用户点名要的，Namecheap 旗下子品牌） ----------
//
// API 依据：Spaceship 官方文档（docs.spaceship.dev）与官方 Go SDK
//（github.com/namecheap/go-spaceship-sdk）。
// 端点 https://spaceship.dev/api/v1，认证 X-API-Key + X-API-Secret
// 两个头（不编码直接放头），权限要 dnsrecords:read 与 dnsrecords:write。
// zone 即域名本身；读 GET /dns/records/{domain}?take=500&skip=0，
// 返回 {items:[{type, name, address, ttl, group:{type}}], total}，
// 只管 group.type=="custom" 的记录；创建/更新是
// PUT /dns/records/{domain} {force:true, items:[{type:"TXT",
// name:"_acme-challenge", address:"<值>", ttl:60}]}；删除是
// DELETE /dns/records/{domain}，body 为数组
// [{type:"TXT", name:"_acme-challenge", address:"<值>"}]。
// ⚠️ 匹配规则（官方文档与 SDK 一致）：记录按 TYPE|name|value 匹配，
// 非 TXT 记录名与值大小写不敏感，TXT 的值大小写敏感——PUT 一条新值
// 不会覆盖同名旧值，而是在同名下多加一条；DELETE 同样按名+类型+值
// 匹配（值须与写入时完全一致）。所以 Create 先按值删残留，Delete
// 必须传当初写入的原值，不能只按名删。
// 错误是 problem+json {"detail":"..."}。

const spaceshipAPI = "https://spaceship.dev/api/v1"

// spaceshipHeaders 返回 Spaceship 的请求头（缺凭证时报清楚去哪填）。
func spaceshipHeaders(s *Server) (map[string]string, error) {
	key := s.cfg.SpaceshipAPIKey
	secret := s.cfg.SpaceshipSecret
	if key == "" || secret == "" {
		return nil, fmt.Errorf("未配置 Spaceship 凭证（设置里填 API Key 与 API Secret，在 spaceship.com/application/api-manager 开，权限勾 dnsrecords:read 与 dnsrecords:write）")
	}
	return map[string]string{"X-API-Key": key, "X-API-Secret": secret}, nil
}

// spaceshipErr 把 Spaceship 的 problem+json 错误拼成带 detail 摘要的错误。
func spaceshipErr(code int, b []byte) error {
	var e struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(b, &e) == nil && e.Detail != "" {
		return fmt.Errorf("Spaceship 报错（HTTP %d）：%s", code, truncateProviderBody([]byte(e.Detail)))
	}
	return fmt.Errorf("Spaceship 报错（HTTP %d）：%s", code, truncateProviderBody(b))
}

// spaceshipFindZone 找域名所在的 zone：从完整域名逐级往上试，
// GET /dns/records/{zone} 返回 200 的第一个就是（不在 Spaceship 名下时 404）。
func spaceshipFindZone(s *Server, domain string) (zone string, headers map[string]string, err error) {
	headers, err = spaceshipHeaders(s)
	if err != nil {
		return "", nil, err
	}
	parts := strings.Split(domain, ".")
	for i := 0; i < len(parts)-1; i++ {
		zone = strings.Join(parts[i:], ".")
		code, b, err := dnsHTTPGet(spaceshipAPI+"/dns/records/"+zone+"?take=1&skip=0", headers)
		if err != nil {
			continue
		}
		if code == 404 {
			continue
		}
		if code >= 300 {
			return "", nil, spaceshipErr(code, b)
		}
		return zone, headers, nil
	}
	return "", nil, fmt.Errorf("Spaceship 里找不到域名 %s 的解析（请先确认该域名在 Spaceship 注册且用它的 DNS）", domain)
}

type spaceshipRecord struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Address string `json:"address"`
	TTL     int    `json:"ttl"`
	Group   struct {
		Type string `json:"type"`
	} `json:"group"`
}

type spaceshipProvider struct{}

func (spaceshipProvider) ID() string { return "spaceship" }

func (spaceshipProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	zone, headers, err := spaceshipFindZone(s, domain)
	if err != nil {
		return nil, err
	}
	code, b, err := dnsHTTPGet(spaceshipAPI+"/dns/records/"+zone+"?take=500&skip=0", headers)
	if err != nil {
		return nil, fmt.Errorf("Spaceship 查 TXT 记录失败：%v", err)
	}
	if code >= 300 {
		return nil, spaceshipErr(code, b)
	}
	var list struct {
		Items []spaceshipRecord `json:"items"`
	}
	if json.Unmarshal(b, &list) != nil {
		return nil, fmt.Errorf("Spaceship 返回解析失败")
	}
	relName := dnsRelativeName(domain, zone)
	var out []dnsTXTRecord
	for _, r := range list.Items {
		// 只管 custom 组（产品自有记录只读不动）；TXT 的值大小写敏感，原样比对。
		if r.Type != "TXT" || r.Name != relName {
			continue
		}
		if r.Group.Type != "" && r.Group.Type != "custom" {
			continue
		}
		out = append(out, dnsTXTRecord{Value: r.Address})
	}
	return out, nil
}

func (p spaceshipProvider) Create(s *Server, domain, value string) (string, error) {
	zone, headers, err := spaceshipFindZone(s, domain)
	if err != nil {
		return "", err
	}
	// 先清残留：PUT 新值不覆盖旧值，必须先按值删掉同名旧 TXT，
	// 否则 _acme-challenge 下越堆越多。没有 address 的条目删不掉，跳过。
	if stale, err := p.ListStale(s, domain); err == nil {
		for _, r := range stale {
			if r.Value == "" {
				continue
			}
			payload, _ := json.Marshal([]map[string]string{
				{"type": "TXT", "name": dnsRelativeName(domain, zone), "address": r.Value},
			})
			_, _, _ = dnsHTTPDeleteBody(spaceshipAPI+"/dns/records/"+zone, payload, "application/json", headers)
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"force": true,
		"items": []map[string]any{
			{"type": "TXT", "name": dnsRelativeName(domain, zone), "address": value, "ttl": 60},
		},
	})
	code, b, err := dnsHTTPPut(spaceshipAPI+"/dns/records/"+zone, payload, "application/json", headers)
	if err != nil {
		return "", fmt.Errorf("Spaceship 写 TXT 记录失败：%v。%s", err, providerManualHint("spaceship", domain))
	}
	if code >= 300 {
		return "", fmt.Errorf("Spaceship 写 TXT 记录失败：%v。%s", spaceshipErr(code, b), providerManualHint("spaceship", domain))
	}
	return "", nil // 没有单条记录 id，删时按值删
}

func (spaceshipProvider) Delete(s *Server, domain, id, value string) error {
	if value == "" {
		return nil // 按值删，没值删不了（也不该删整条名，免得动到别人的值）
	}
	zone, headers, err := spaceshipFindZone(s, domain)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal([]map[string]string{
		{"type": "TXT", "name": dnsRelativeName(domain, zone), "address": value},
	})
	code, b, err := dnsHTTPDeleteBody(spaceshipAPI+"/dns/records/"+zone, payload, "application/json", headers)
	if err != nil {
		return err
	}
	if code == 404 {
		return nil // 已经没了
	}
	if code >= 300 {
		return spaceshipErr(code, b)
	}
	return nil
}
