package main

import _ "embed"

//go:embed prompts/system-prompt.md
var systemPromptContent string

//go:embed prompts/yolo-prompt.md
var yoloPromptContent string

//go:embed prompts/gemini-system.md
var geminiSystemPrompt string

//go:embed prompts/research-routing.md
var researchRoutingContent string
