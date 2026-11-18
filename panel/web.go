package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web
var webFS embed.FS

func serveEmbeddedWeb(w http.ResponseWriter, r *http.Request) {
	// 前端文件内嵌在二进制里（panel/web/）
	if _, err := fs.Stat(webFS, "web/index.html"); err == nil {
		data, err := webFS.ReadFile("web/index.html")
		if err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
	}
	// 静态文件（app.js / style.css 等）
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p != "" && p != "/" {
		if data, err := webFS.ReadFile("web/" + p); err == nil {
			w.Header().Set("Content-Type", contentTypeFor(p))
			_, _ = w.Write(data)
			return
		}
	}
	data, _ := webFS.ReadFile("web/placeholder.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
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
