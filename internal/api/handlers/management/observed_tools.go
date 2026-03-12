package management

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/observedtools"
)

// GetObservedTools returns all tool schemas observed in requests passing through the proxy.
func (h *Handler) GetObservedTools(c *gin.Context) {
	tools := observedtools.Global().List()
	c.JSON(200, gin.H{"observed-tools": tools})
}

// DeleteObservedTools clears all observed tool schemas.
func (h *Handler) DeleteObservedTools(c *gin.Context) {
	observedtools.Global().Clear()
	c.JSON(200, gin.H{"status": "ok"})
}
