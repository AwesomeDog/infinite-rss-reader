package main

import "embed"

//go:embed embed/index.html
var indexHTML []byte

//go:embed add-on/*
var addonFS embed.FS

//go:embed embed/infrss.json
var manifestTemplate []byte
