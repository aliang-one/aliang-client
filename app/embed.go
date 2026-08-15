package app

import (
	"embed"
)

//go:embed website/dist
var WebsiteFS embed.FS
