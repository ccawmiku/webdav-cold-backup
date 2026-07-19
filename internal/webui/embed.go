package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dist/*
var assets embed.FS

func Handler() http.Handler {
	directory, _ := fs.Sub(assets, "dist")
	files := http.FileServer(http.FS(directory))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		clean := strings.TrimPrefix(request.URL.Path, "/")
		if clean != "" {
			if file, err := directory.Open(clean); err == nil {
				_ = file.Close()
				files.ServeHTTP(writer, request)
				return
			}
		}
		request.URL.Path = "/"
		files.ServeHTTP(writer, request)
	})
}
