package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var EmbedFS embed.FS

func GetFileSystem() http.FileSystem {
	fsys, err := fs.Sub(EmbedFS, "static")
	if err != nil {
		panic(err)
	}
	return http.FS(fsys)
}
