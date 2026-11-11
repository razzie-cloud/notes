package web

import (
	"embed"
	"io/fs"
)

//go:embed templates.html
var Templates string

//go:embed static/*
var assets embed.FS
var Static fs.FS

func init() { Static, _ = fs.Sub(assets, "static") }
