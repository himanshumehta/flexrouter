// Package all registers every built-in provider. A new provider is a new
// package plus one import line here; the core engine does not change.
package all

import (
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/anthropicapi"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/claude"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/codex"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/copilot"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/cursor"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/deepseek"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/flexrouter"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/kilo"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/moonshot"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/ollama"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/openaiapi"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/openrouter"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/xai"
)
