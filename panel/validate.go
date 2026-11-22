package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var domainRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// validIPOrDomain 校验落地地址：IPv4 或域名。
func validIPOrDomain(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	if ip := net.ParseIP(s); ip != nil && ip.To4() != nil {
		return true
	}
	// 纯数字加点（如 256.1.1.1）不是合法 IP 也不是域名
	allNumDot := true
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c == '.') {
			allNumDot = false
			break
		}
	}
	if allNumDot {
		return false
	}
	return domainRe.MatchString(s)
}

func isIP(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}

func validPort(p int) bool { return p >= 1 && p <= 65535 }

func validDomain(s string) bool { return domainRe.MatchString(s) }

// parseCustomPorts 解析 preset 3 的自定义端口： "443,8388,8443:9443" → PortSpec 列表（TCP，WANT_UDP 时另加 UDP）。
func parseCustomPorts(s string, wantUDP bool) ([]PortSpec, error) {
	var out []PortSpec
	for _, e := range strings.Split(s, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		lp, rp := e, e
		if strings.Contains(e, ":") {
			parts := strings.SplitN(e, ":", 2)
			lp, rp = parts[0], parts[1]
		}
		l, err1 := strconv.Atoi(lp)
		r, err2 := strconv.Atoi(rp)
		if err1 != nil || err2 != nil || !validPort(l) || !validPort(r) {
			return nil, fmt.Errorf("端口不合法：%s（1-65535）", e)
		}
		out = append(out, PortSpec{Proto: "tcp", Listen: l, Remote: r})
		if wantUDP {
			out = append(out, PortSpec{Proto: "udp", Listen: l, Remote: r})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("自定义端口为空")
	}
	return out, nil
}

func portsToSpecString(ports []PortSpec) string {
	var parts []string
	for _, p := range ports {
		parts = append(parts, fmt.Sprintf("%s:%d:%d", p.Proto, p.Listen, p.Remote))
	}
	return strings.Join(parts, " ")
}

// ---------- 节点链接解析 ----------

type ParsedLink struct {
	Scheme string
	Host   string
	Port   int
	SNI    string
	// Name 备注名：vless/trojan/ss 是 # 后面的片段，vmess 是 JSON 里的 ps；
	// url.Parse 已把百分号编码解开，这里是解码后的原文。
	Name string
}

// parseNodeLink 解析 vless/trojan/ss/vmess/hysteria2/tuic 链接，取出协议/地址/端口/SNI。
// HY2 链接：hysteria2://<密码>@<地址>:<端口>?sni=<域名>&alpn=h3（也认 hy2://
// 别名、obfs=salamander + obfs-password）；TUIC 链接：
// tuic://<uuid>:<密码>@<地址>:<端口>?congestion_control=bbr&alpn=h3&sni=<域名>。
// （落地 install.sh 生成的格式；与 1.3.39 订阅输出认的格式一致。）
// HY2 没密码、TUIC 没 uuid/密码的链接不算有效节点，直接报错（订阅侧同
// 样是字段缺失就跳过不断链，注册侧在入口就拦住）。
func parseNodeLink(link string) (*ParsedLink, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Scheme == "" {
		return nil, fmt.Errorf("链接格式不对")
	}
	scheme := strings.ToLower(u.Scheme)
	pl := &ParsedLink{Scheme: scheme}
	switch scheme {
	case "vless", "trojan":
		host := u.Hostname()
		p, _ := strconv.Atoi(u.Port())
		pl.Host, pl.Port = host, p
		pl.Name = u.Fragment
		pl.SNI = u.Query().Get("sni")
		if pl.SNI == "" {
			pl.SNI = u.Query().Get("host")
		}
		if pl.SNI == "" && !isIP(host) {
			pl.SNI = host
		}
	case "hysteria2", "hy2":
		host := u.Hostname()
		p, _ := strconv.Atoi(u.Port())
		pl.Host, pl.Port = host, p
		pl.Name = u.Fragment
		if u.User == nil || u.User.Username() == "" {
			return nil, fmt.Errorf("hysteria2 链接里没有密码")
		}
		pl.SNI = u.Query().Get("sni")
		if pl.SNI == "" {
			pl.SNI = u.Query().Get("host")
		}
		if pl.SNI == "" && !isIP(host) {
			pl.SNI = host
		}
	case "tuic":
		host := u.Hostname()
		p, _ := strconv.Atoi(u.Port())
		pl.Host, pl.Port = host, p
		pl.Name = u.Fragment
		if u.User == nil || u.User.Username() == "" {
			return nil, fmt.Errorf("tuic 链接里没有 uuid")
		}
		if _, hasPw := u.User.Password(); !hasPw {
			return nil, fmt.Errorf("tuic 链接里没有密码")
		}
		pl.SNI = u.Query().Get("sni")
		if pl.SNI == "" {
			pl.SNI = u.Query().Get("host")
		}
		if pl.SNI == "" && !isIP(host) {
			pl.SNI = host
		}
	case "ss":
		pl.Name = u.Fragment
		if u.User != nil {
			// SIP002：ss://user@host:port（userinfo 可能是 base64 的 method:password）
			host := u.Hostname()
			p, _ := strconv.Atoi(u.Port())
			pl.Host, pl.Port = host, p
		} else if u.Host != "" {
			// host 位置可能是明文 host:port（SIP002），也可能是整段 base64
			// 的传统形式 ss://base64(method:password@host:port)。
			// 先试 base64 解码：解出来带 @ 且 host:port 合法就是传统形式。
			if raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(u.Host, "=")); err == nil {
				if at := strings.LastIndex(string(raw), "@"); at >= 0 {
					hp := string(raw[at+1:])
					h, pt, _ := net.SplitHostPort(hp)
					p, _ := strconv.Atoi(pt)
					if h != "" && p > 0 {
						pl.Host, pl.Port = h, p
					}
				}
			}
			if pl.Port == 0 {
				host := u.Hostname()
				p, _ := strconv.Atoi(u.Port())
				pl.Host, pl.Port = host, p
			}
		} else {
			// 传统 base64 形式（Opaque，无 //）：ss:base64(method:password@host:port)
			raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(u.Opaque, "="))
			if err != nil {
				raw, err = base64.StdEncoding.DecodeString(u.Opaque)
				if err != nil {
					return nil, fmt.Errorf("ss 链接解码失败")
				}
			}
			// method:password@host:port
			at := strings.LastIndex(string(raw), "@")
			if at < 0 {
				return nil, fmt.Errorf("ss 链接格式不对")
			}
			hp := string(raw[at+1:])
			h, pt, _ := net.SplitHostPort(hp)
			p, _ := strconv.Atoi(pt)
			pl.Host, pl.Port = h, p
		}
	case "vmess":
		// vmess://<base64>：base64 在 URL 的 host 部分，Opaque 为空
		b64raw := strings.TrimPrefix(strings.TrimSpace(link), scheme+"://")
		b64raw = strings.SplitN(b64raw, "#", 2)[0]
		b64raw = strings.SplitN(b64raw, "?", 2)[0]
		b64 := strings.TrimRight(b64raw, "=")
		raw, err := base64.RawURLEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("vmess 链接解码失败")
		}
		var j map[string]any
		if err := json.Unmarshal(raw, &j); err != nil {
			return nil, fmt.Errorf("vmess 链接解码失败")
		}
		pl.Host, _ = j["add"].(string)
		if s, ok := j["ps"].(string); ok {
			pl.Name = s
		}
		switch v := j["port"].(type) {
		case string:
			pl.Port, _ = strconv.Atoi(v)
		case float64:
			pl.Port = int(v)
		}
		if s, ok := j["sni"].(string); ok && s != "" {
			pl.SNI = s
		} else if s, ok := j["host"].(string); ok {
			pl.SNI = s
		}
		if pl.SNI == "" && !isIP(pl.Host) {
			pl.SNI = pl.Host
		}
	default:
		return nil, fmt.Errorf("不支持的链接协议：%s（只支持 vless/trojan/ss/vmess/hysteria2/tuic）", scheme)
	}
	if pl.Host == "" {
		return nil, fmt.Errorf("链接里没有地址")
	}
	return pl, nil
}

// linkHostDomain 取链接的域名部分（用于 add-node 第 2 步校验：必须等于填的子域名）。
func linkHostDomain(link string) (string, error) {
	pl, err := parseNodeLink(link)
	if err != nil {
		return "", err
	}
	// vless/trojan 的 host 可能是域名；vmess 的 add 可能是域名；sni 也可作为域名依据
	if !isIP(pl.Host) {
		return pl.Host, nil
	}
	if pl.SNI != "" {
		return pl.SNI, nil
	}
	return pl.Host, nil
}
