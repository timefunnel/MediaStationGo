// Package handler — subtitle endpoints.
package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ShukeBta/MediaStationGo/internal/middleware"
	"github.com/ShukeBta/MediaStationGo/internal/service"
)

type invalidateCloudSubtitleCacheRequest struct {
	MediaID  string `json:"media_id"`
	Provider string `json:"provider"`
}

type subtitleSearchRequest struct {
	Limit int `json:"limit"`
}

type subtitleCandidateRequest struct {
	SearchSessionID string `json:"search_session_id"`
	CandidateID     string `json:"candidate_id"`
}

func listSubtitlesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		media, err := svc.Media.GetMedia(c.Request.Context(), c.Param("id"))
		if err != nil || media == nil || !mediaVisibleForRequest(c, svc, media) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
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
		media, err := svc.Media.GetMedia(c.Request.Context(), c.Param("id"))
		if err != nil || media == nil || !mediaVisibleForRequest(c, svc, media) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
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

func deleteSubtitleHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := strings.TrimSpace(c.Query("path"))
		if path == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing subtitle path"})
			return
		}
		if svc == nil || svc.Subtitle == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service unavailable"})
			return
		}
		if err := svc.Subtitle.Delete(c.Request.Context(), c.Param("id"), path); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": true})
	}
}

func searchSubtitleCandidatesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in subtitleSearchRequest
		if c.Request.ContentLength != 0 {
			if err := c.ShouldBindJSON(&in); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
		if svc == nil || svc.Subtitle == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service unavailable"})
			return
		}
		result, err := svc.Subtitle.SearchCandidates(c.Request.Context(), middleware.GetUserID(c), c.Param("id"), in.Limit)
		if err != nil {
			writeSubtitlePipelineError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

func previewSubtitleCandidateHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil || svc.Subtitle == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service unavailable"})
			return
		}
		in, ok := bindSubtitleCandidateRequest(c)
		if !ok {
			return
		}
		result, err := svc.Subtitle.PreviewCandidate(
			c.Request.Context(), middleware.GetUserID(c), c.Param("id"), in.SearchSessionID, in.CandidateID,
		)
		if err != nil {
			writeSubtitlePipelineError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

func applySubtitleCandidateHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil || svc.Subtitle == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service unavailable"})
			return
		}
		in, ok := bindSubtitleCandidateRequest(c)
		if !ok {
			return
		}
		result, err := svc.Subtitle.ApplyCandidate(
			c.Request.Context(), middleware.GetUserID(c), c.Param("id"), in.SearchSessionID, in.CandidateID,
		)
		if err != nil {
			writeSubtitlePipelineError(c, err)
			return
		}
		tracks, err := svc.Subtitle.Discover(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "subtitle saved but cannot be discovered: " + err.Error()})
			return
		}
		visible := false
		for _, track := range tracks {
			if result.Filename != "" && track.Name == result.Filename {
				visible = true
				break
			}
		}
		if !visible {
			c.JSON(http.StatusBadGateway, gin.H{"error": "subtitle saved by pipeline but is not visible in MediaStationGo subtitle cache"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"result": result, "tracks": tracks})
	}
}

func bindSubtitleCandidateRequest(c *gin.Context) (subtitleCandidateRequest, bool) {
	var in subtitleCandidateRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return subtitleCandidateRequest{}, false
	}
	in.SearchSessionID = strings.TrimSpace(in.SearchSessionID)
	in.CandidateID = strings.TrimSpace(in.CandidateID)
	if in.SearchSessionID == "" || in.CandidateID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "search_session_id and candidate_id are required"})
		return subtitleCandidateRequest{}, false
	}
	return in, true
}

func writeSubtitlePipelineError(c *gin.Context, err error) {
	var pipelineStatus interface{ HTTPStatus() int }
	if errors.As(err, &pipelineStatus) {
		status := pipelineStatus.HTTPStatus()
		if status >= 400 && status < 500 {
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
}
