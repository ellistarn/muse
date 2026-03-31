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

//go:embed theme.md
var Theme string

//go:embed observe-human.md
var ObserveHuman string

//go:embed judge-observable.md
var JudgeObservable string

//go:embed judge-epistemic.md
var JudgeEpistemic string

//go:embed generate-eval.md
var GenerateEval string
