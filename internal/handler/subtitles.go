// Package handler — subtitle endpoints.
package handler

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/middleware"
	"github.com/ShukeBta/MediaStationGo/internal/model"
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

type subtitleASRRequest struct {
	SourceLanguage      string `json:"source_language"`
	TranslationProvider string `json:"translation_provider"`
	TranslationModel    string `json:"translation_model"`
}

type subtitleTranslationRequest struct {
	Provider string                               `json:"provider"`
	Model    string                               `json:"model"`
	Segments []service.SubtitleTranslationSegment `json:"segments"`
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

func createSubtitleASRHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		media, err := svc.Media.GetMedia(c.Request.Context(), c.Param("id"))
		if err != nil || media == nil || !mediaVisibleForRequest(c, svc, media) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		var in subtitleASRRequest
		if c.Request.ContentLength != 0 {
			if err := c.ShouldBindJSON(&in); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
		task, err := svc.Subtitle.CreateASRTask(
			c.Request.Context(), middleware.GetUserID(c), media.ID, in.SourceLanguage,
			in.TranslationProvider, in.TranslationModel,
		)
		if err != nil {
			writeSubtitlePipelineError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, task)
	}
}

func listSubtitleASRProfilesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		profiles, err := svc.Subtitle.ListASRProfiles(c.Request.Context())
		if err != nil {
			writeSubtitlePipelineError(c, err)
			return
		}
		c.JSON(http.StatusOK, service.SubtitleASRProfileList{Items: profiles})
	}
}

func retrySubtitleASRTaskHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in subtitleASRRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		task, err := svc.Subtitle.RetryASRTask(
			c.Request.Context(), middleware.GetUserID(c), c.Param("task_id"),
			in.TranslationProvider, in.TranslationModel,
		)
		if err != nil {
			writeSubtitlePipelineError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, task)
	}
}

func deleteSubtitleASRTaskHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.Subtitle.DeleteASRTask(
			c.Request.Context(), middleware.GetUserID(c), c.Param("task_id"),
		); err != nil {
			writeSubtitlePipelineError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": true})
	}
}

func getSubtitleASRHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		task, err := svc.Subtitle.GetASRTask(c.Request.Context(), middleware.GetUserID(c), c.Param("task_id"))
		if err != nil {
			writeSubtitlePipelineError(c, err)
			return
		}
		if task.MediaID != c.Param("id") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusOK, task)
	}
}

func listSubtitleASRTasksHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		tasks, err := svc.Subtitle.ListASRTasks(c.Request.Context(), 50)
		if err != nil {
			writeSubtitlePipelineError(c, err)
			return
		}
		mediaIDs := make([]string, 0, len(tasks))
		for i := range tasks {
			mediaIDs = append(mediaIDs, tasks[i].MediaID)
		}
		mediaRows, mediaErr := svc.Media.GetMediaByIDs(c.Request.Context(), mediaIDs)
		if mediaErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": mediaErr.Error()})
			return
		}
		mediaByID := make(map[string]model.Media, len(mediaRows))
		for i := range mediaRows {
			mediaByID[mediaRows[i].ID] = mediaRows[i]
		}
		for i := range tasks {
			media, ok := mediaByID[tasks[i].MediaID]
			if !ok {
				continue
			}
			tasks[i].MediaAvailable = true
			tasks[i].MediaTitle = strings.TrimSpace(media.DisplayTitle)
			if tasks[i].MediaTitle == "" {
				tasks[i].MediaTitle = strings.TrimSpace(media.Title)
			}
			if tasks[i].MediaTitle == "" {
				tasks[i].MediaTitle = strings.TrimSpace(media.OriginalName)
			}
			normalizedPath := strings.Trim(strings.ReplaceAll(strings.TrimSpace(media.Path), "\\", "/"), "/")
			if slash := strings.LastIndex(normalizedPath, "/"); slash >= 0 {
				normalizedPath = normalizedPath[slash+1:]
			}
			tasks[i].MediaFilename = normalizedPath
		}
		c.JSON(http.StatusOK, service.SubtitleASRTaskList{Items: tasks})
	}
}

func pipelineASRAudioHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil || svc.Stream == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "media stream service unavailable"})
			return
		}
		stream, wait, info, err := svc.Stream.StartASRAudioExtraction(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		defer stream.Close()
		c.Header("Content-Type", "audio/mpeg")
		c.Header("Content-Disposition", `attachment; filename="asr-audio.mp3"`)
		c.Header("Cache-Control", "no-store")
		if info.DurationSeconds > 0 {
			c.Header("X-Media-Duration-Seconds", strconv.Itoa(info.DurationSeconds))
		}
		if info.BitrateBPS > 0 {
			c.Header("X-ASR-Audio-Bitrate", strconv.Itoa(info.BitrateBPS))
		}
		copied, copyErr := io.Copy(c.Writer, stream)
		waitErr := wait()
		if copyErr == nil && waitErr == nil && copied > 0 {
			return
		}
		if copied == 0 && !c.Writer.Written() {
			c.Writer.Header().Del("Content-Type")
			c.Writer.Header().Del("Content-Disposition")
			if copyErr != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": "audio extraction stream failed: " + copyErr.Error()})
				return
			}
			if waitErr != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": waitErr.Error()})
				return
			}
			c.JSON(http.StatusBadGateway, gin.H{"error": "audio extraction returned no data"})
			return
		}
		if svc.Log != nil {
			svc.Log.Error("ASR audio extraction stream failed", zap.Int64("bytes", copied), zap.Error(errors.Join(copyErr, waitErr)))
		}
	}
}

func pipelineTranslateSubtitlesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in subtitleTranslationRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		result, err := svc.Subtitle.TranslateSegments(
			c.Request.Context(), in.Provider, in.Model, in.Segments,
		)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
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
		if (status >= 400 && status < 500) || status == http.StatusServiceUnavailable {
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
}
