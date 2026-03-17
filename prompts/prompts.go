package prompts

import _ "embed"

//go:embed observe-extract.md
var ObserveExtract string

//go:embed observe-summarize.md
var ObserveSummarize string

//go:embed observe-refine.md
var ObserveRefine string

//go:embed learn.md
var Learn string

//go:embed diff.md
var Diff string

//go:embed muse.md
var Muse string

//go:embed tool.md
var Tool string

//go:embed classify.md
var Classify string

//go:embed synthesize.md
var Synthesize string

//go:embed merge.md
var Merge string
