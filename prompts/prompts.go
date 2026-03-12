package prompts

import _ "embed"

//go:embed reflect-extract.md
var ReflectExtract string

//go:embed reflect-summarize.md
var ReflectSummarize string

//go:embed reflect-refine.md
var ReflectRefine string

//go:embed learn.md
var Learn string

//go:embed shade.md
var Shade string

//go:embed tool.md
var Tool string
