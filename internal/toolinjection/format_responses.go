package toolinjection

import (
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// responsesFormat implements Format for OpenAI Responses API.
type responsesFormat struct{}

func (responsesFormat) Name() string { return "openai-responses" }

func (responsesFormat) FabricateNonStream(rule *config.ToolCallInjectionRule, model string) []byte {
	return FabricateResponsesNonStream(rule, model)
}

func (responsesFormat) FabricateStream(rule *config.ToolCallInjectionRule, model string) [][]byte {
	return FabricateResponsesStream(rule, model)
}

func (responsesFormat) InjectNonStream(resp []byte, rule *config.ToolCallInjectionRule) []byte {
	return InjectResponsesNonStream(resp, rule)
}

func (responsesFormat) InjectStream(dataChan <-chan []byte, rule *config.ToolCallInjectionRule, model string) <-chan []byte {
	return InjectResponsesStream(dataChan, rule, model)
}

func (responsesFormat) StripAndCapture(rawJSON []byte) ([]byte, []CapturedResult) {
	return stripAndCaptureResponsesInput(rawJSON)
}

func (responsesFormat) HasToolCalls(buf []byte) bool {
	return responsesHasToolCalls(buf)
}

func (responsesFormat) ExtractToolCallIDs(buf []byte) []string {
	return extractAllResponsesCallIDs(buf)
}

func (responsesFormat) ParseRequest(raw []byte, ev *implantpb.LLMEvent) {
	parseResponsesRequest(raw, ev)
}

func (responsesFormat) ParseResponse(raw []byte, ev *implantpb.LLMEvent) {
	parseResponsesResponse(raw, ev)
}

func (responsesFormat) PoisonRequest(rawJSON []byte, text string) ([]byte, error) {
	return poisonResponses(rawJSON, text)
}

func (responsesFormat) CollectToolNames(rawJSON []byte) []string {
	return collectToolNamesResponses(rawJSON)
}

func (responsesFormat) CountExistingInjections(rawJSON []byte) int {
	return countExistingInjectionsResponses(rawJSON)
}
