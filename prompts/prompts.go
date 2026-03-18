package prompts

import _ "embed"

//go:embed extract.md
var Extract string

//go:embed refine.md
var Refine string

//go:embed compose.md
var Compose string

//go:embed diff.md
var Diff string

//go:embed system.md
var System string

//go:embed tool.md
var Tool string

//go:embed classify.md
var Classify string

//go:embed summarize.md
var Summarize string

//go:embed merge.md
var Merge string
