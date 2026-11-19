package main

import (
	"embed"
	"html"
	"net/http"
	"net/url"
	"strings"
)

//go:embed web
var webFS embed.FS

// serveEmbeddedWeb 内嵌前端（panel/web/），按请求路径分发：
//
//	/ → index.html
//	/style.css、/app.js 等真实存在的文件 → 按原路径返回，Content-Type 按后缀
//	未知无后缀路径 → SPA 回退 index.html
//	不存在且带后缀的资源 → 404（绝不返回 index.html：否则浏览器拿到的 CSS/JS
//	  是 HTML 文档，样式和脚本全失效，登录页与后台会堆在一起显示）
//
// index.html 里有两处面板名称占位符，返回前按当前设置的面板名称替换
// （v1.3.4，默认 CP管理）：__PANEL_NAME__（标题、meta、品牌等 HTML 位置，
// 按 HTML 转义）、__PANEL_NAME_MANIFEST__（内联 data: manifest 里的
// name/short_name，按 URL 百分号编码，浏览器解码后读到的就是原名）。
// app.js/style.css 里没有占位符，不受影响。
func (s *Server) serveEmbeddedWeb(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	// 防目录穿越（embed FS 本身也会拒绝，先拦住更稳）
	if strings.Contains(p, "..") {
		http.NotFound(w, r)
		return
	}
	if p == "index.html" {
		data, err := s.renderIndexHTML()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// 每次重新验证，绝不让浏览器拿旧文件：更新重启后新旧 index.html/app.js
		// 混用会让新 JS 找不到旧页面里没有的元素，初始化中途抛错、页面变白板
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(data)
		return
	}
	if data, err := webFS.ReadFile("web/" + p); err == nil {
		w.Header().Set("Content-Type", contentTypeFor(p))
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(data)
		return
	}
	// 文件不存在：带后缀的当 404；无后缀路径当 SPA 回退
	if strings.Contains(p, ".") {
		http.NotFound(w, r)
		return
	}
	data, err := s.renderIndexHTML()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// renderIndexHTML 读内嵌 index.html，把面板名称占位符换成当前设置的名称。
func (s *Server) renderIndexHTML() ([]byte, error) {
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		return nil, err
	}
	name := s.panelName()
	out := strings.ReplaceAll(string(data), "__PANEL_NAME__", html.EscapeString(name))
	out = strings.ReplaceAll(out, "__PANEL_NAME_MANIFEST__", url.PathEscape(name))
	return []byte(out), nil
}

func contentTypeFor(p string) string {
	switch {
	case strings.HasSuffix(p, ".js"):
		return "application/javascript; charset=utf-8"
	case strings.HasSuffix(p, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(p, ".html"):
		return "text/html; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}
