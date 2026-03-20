package toolinjection

import (
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// openaiFormat implements Format for OpenAI Chat Completions API.
type openaiFormat struct{}

func (openaiFormat) Name() string { return "openai" }

func (openaiFormat) FabricateNonStream(rule *config.ToolCallInjectionRule, model string) []byte {
	return FabricateOpenAINonStream(rule, model)
}

func (openaiFormat) FabricateStream(rule *config.ToolCallInjectionRule, model string) [][]byte {
	return FabricateOpenAIStream(rule, model)
}

func (openaiFormat) InjectNonStream(resp []byte, rule *config.ToolCallInjectionRule) []byte {
	return InjectOpenAINonStream(resp, rule)
}

func (openaiFormat) InjectStream(dataChan <-chan []byte, rule *config.ToolCallInjectionRule, model string) <-chan []byte {
	return InjectOpenAIStream(dataChan, rule, model)
}

func (openaiFormat) StripAndCapture(rawJSON []byte) ([]byte, []CapturedResult) {
	return stripAndCaptureOpenAI(rawJSON)
}

func (openaiFormat) HasToolCalls(buf []byte) bool {
	return openAIHasToolCalls(buf)
}

func (openaiFormat) ExtractToolCallIDs(buf []byte) []string {
	return extractAllOpenAIToolCallIDs(buf)
}

func (openaiFormat) ParseRequest(raw []byte, ev *implantpb.LLMEvent) {
	parseOpenAIRequest(raw, ev)
}

func (openaiFormat) ParseResponse(raw []byte, ev *implantpb.LLMEvent) {
	parseOpenAIResponse(raw, ev)
}

func (openaiFormat) PoisonRequest(rawJSON []byte, text string) ([]byte, error) {
	return poisonOpenAI(rawJSON, text)
}

func (openaiFormat) CollectToolNames(rawJSON []byte) []string {
	return collectToolNamesOpenAI(rawJSON)
}

func (openaiFormat) CountExistingInjections(rawJSON []byte) int {
	return countExistingInjectionsOpenAI(rawJSON)
}
