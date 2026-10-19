package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ShukeBta/MediaStationGo/internal/service"
)

// serveMigratedCloudArtwork runs before the legacy cloud cache/resolver.
// Reusing the original request preserves width, height, quality, and HEAD.
func serveMigratedCloudArtwork(svc *service.Container, c *gin.Context, provider, ref string) bool {
	if svc == nil || svc.Media == nil {
		return false
	}
	raw, found, err := svc.Media.ResolveCloudArtworkAlias(c.Request.Context(), provider, ref)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, service.ErrCloudArtworkTargetGone) {
			status = http.StatusGone
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return true
	}
	if !found {
		return false
	}
	if svc.ImageProxy == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "image proxy unavailable"})
		return true
	}
	if err := svc.ImageProxy.Serve(c.Request.Context(), c.Writer, c.Request, raw); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
	}
	return true
}
