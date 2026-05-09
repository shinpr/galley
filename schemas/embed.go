package schemas

import _ "embed"

//go:embed supervisor-verdict.schema.json
var SupervisorVerdict string

//go:embed claude-result.schema.json
var ClaudeResult string
