package prompts

import _ "embed"

//go:embed observe.md
var Observe string

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

//go:embed label.md
var Label string

//go:embed summarize.md
var Summarize string

//go:embed compose-clustered.md
var ComposeClustered string

//go:embed theme-identify.md
var ThemeIdentify string

//go:embed theme-map.md
var ThemeMap string

//go:embed observe-human.md
var ObserveHuman string

//go:embed judge-dimensions.md
var JudgeDimensions string

//go:embed judge-preference.md
var JudgePreference string

//go:embed generate-eval.md
var GenerateEval string

//go:embed thesis.md
var Thesis string

//go:embed triage.md
var Triage string
