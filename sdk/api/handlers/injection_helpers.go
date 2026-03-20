package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/toolinjection"
)

// SetSSEHeaders sets the standard headers for Server-Sent Events streaming.
func SetSSEHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")
}

// RequireFlusher attempts to obtain an http.Flusher from the response writer.
// Returns (flusher, true) on success, or writes an error response and returns (nil, false).
func RequireFlusher(c *gin.Context) (http.Flusher, bool) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error: ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
	}
	return flusher, ok
}

// ApplyNonStreamInjection applies a tool call injection to a non-streaming response.
// If injection is nil, returns resp unchanged.
// For "replace" timing: fabricates a complete response, discarding the real one.
// For "append" timing: appends a tool call to the real response.
func ApplyNonStreamInjection(resp []byte, injection *config.ToolCallInjectionRule, format, model string) []byte {
	if injection == nil {
		return resp
	}
	f := toolinjection.GetFormat(format)
	if f == nil {
		return resp
	}
	if injection.Timing == "replace" {
		return f.FabricateNonStream(injection, model)
	}
	return f.InjectNonStream(resp, injection)
}

// ApplyStreamInjection wraps data and error channels for streaming injection.
// If injection is nil, returns channels unchanged.
// For "replace" timing: drains upstream channels, returns fabricated stream.
// For "append" timing: wraps dataChan with format-specific stream injector.
func ApplyStreamInjection(
	dataChan <-chan []byte,
	errChan <-chan *interfaces.ErrorMessage,
	injection *config.ToolCallInjectionRule,
	format, model string,
) (<-chan []byte, <-chan *interfaces.ErrorMessage) {
	if injection == nil {
		return dataChan, errChan
	}
	f := toolinjection.GetFormat(format)
	if f == nil {
		return dataChan, errChan
	}

	if injection.Timing == "replace" {
		// Drain upstream channels in background.
		go func() { for range dataChan {} }()
		if errChan != nil {
			go func() { for range errChan {} }()
		}

		// Fabricate a complete stream.
		chunks := f.FabricateStream(injection, model)
		fakeChan := make(chan []byte, len(chunks))
		for _, chunk := range chunks {
			fakeChan <- chunk
		}
		close(fakeChan)
		return fakeChan, nil
	}

	// Append mode: wrap the real stream.
	return f.InjectStream(dataChan, injection, model), errChan
}
