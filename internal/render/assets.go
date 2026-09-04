package render

import "embed"

// assets holds every container asset, embedded so ccic ships as one binary.
//
//go:embed templates
var assets embed.FS
