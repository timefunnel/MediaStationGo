package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/ShukeBta/MediaStationGo/internal/service"
	"github.com/gin-gonic/gin"
)

func bindProbeExchange(c *gin.Context, value any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.ProbeExchangeMaxBytes)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "任务文件结构无效、包含未知字段或超过 64 MiB"})
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "任务文件包含额外内容"})
		return false
	}
	return true
}

func registerProbeExchangeRoutes(queue *gin.RouterGroup, svc *service.MediaProbeQueueService) {
	queue.POST("/exchange/manifest", func(c *gin.Context) {
		var req struct {
			LibraryIDs []string `json:"library_ids"`
		}
		if !bindProbeExchange(c, &req) {
			return
		}
		if len(req.LibraryIDs) > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "最多选择 100 个媒体库"})
			return
		}
		manifest, err := svc.ExportExchangeManifest(c.Request.Context(), req.LibraryIDs)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.Header("Content-Disposition", "attachment; filename=media-probe-manifest-"+manifest.ID+".json")
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, manifest)
	})
	queue.POST("/exchange/preview", func(c *gin.Context) {
		var bundle service.ProbeExchangeBundle
		if !bindProbeExchange(c, &bundle) {
			return
		}
		preview, err := svc.PreviewExchange(c.Request.Context(), &bundle)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, preview)
	})
	queue.POST("/exchange/apply", func(c *gin.Context) {
		var req struct {
			Bundle    service.ProbeExchangeBundle `json:"bundle"`
			Digest    string                      `json:"preview_digest"`
			Confirmed bool                        `json:"confirmed"`
		}
		if !bindProbeExchange(c, &req) {
			return
		}
		preview, err := svc.ApplyExchange(c.Request.Context(), &req.Bundle, req.Digest, req.Confirmed)
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, preview)
	})
}
