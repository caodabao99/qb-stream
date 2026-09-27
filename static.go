package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFS embed.FS

// handleSPA 服务 SPA 入口页（内嵌，无需外部文件）
func handleSPA(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "前端资源缺失: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// handleAssets 服务内嵌静态资源（vue / app.js / style.css）
func handleAssets(w http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.StripPrefix("/assets/", http.FileServer(http.FS(sub))).ServeHTTP(w, r)
}
