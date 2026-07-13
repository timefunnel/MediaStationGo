// Package handler — subtitle endpoints.
package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ShukeBta/MediaStationGo/internal/service"
)

type invalidateCloudSubtitleCacheRequest struct {
	MediaID  string `json:"media_id"`
	Provider string `json:"provider"`
}

func listSubtitlesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		tracks, err := svc.Subtitle.Discover(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		if tracks == nil {
			tracks = []service.SubtitleTrack{}
		}
		c.JSON(http.StatusOK, gin.H{"tracks": tracks})
	}
}

func serveSubtitleHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Query("path")
		if path == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing path"})
			return
		}
		c.Header("Content-Type", "text/vtt; charset=utf-8")
		c.Header("Cache-Control", "public, max-age=3600")
		if err := svc.Subtitle.Serve(c.Request.Context(), c.Param("id"), path, c.Writer); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
	}
}

func invalidateCloudSubtitleCacheHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil || svc.Subtitle == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service unavailable"})
			return
		}
		var in invalidateCloudSubtitleCacheRequest
		if c.Request.ContentLength != 0 {
			if err := c.ShouldBindJSON(&in); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
		mediaID := strings.TrimSpace(in.MediaID)
		provider := strings.TrimSpace(in.Provider)
		invalidated := svc.Subtitle.InvalidateCloudDiscovery(mediaID, provider)
		c.JSON(http.StatusOK, gin.H{
			"invalidated": invalidated,
			"media_id":    mediaID,
			"provider":    provider,
		})
	}
}
