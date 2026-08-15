package tutorials

import "embed"

// FS contains bundled tutorial markdown files for en and zh_CN locales.
//
//go:embed en zh_CN
var FS embed.FS
