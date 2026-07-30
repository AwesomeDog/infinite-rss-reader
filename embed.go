package infrssassets

import "embed"

//go:embed embed/index.html
var IndexHTML []byte

//go:embed add-on/*
var AddonFS embed.FS

//go:embed embed/infrss.json
var ManifestTemplate []byte
