package handler

import (
	"net/http"

	"github.com/ShukeBta/MediaStationGo/internal/service"
	"github.com/gin-gonic/gin"
)

func registerProbeCampaignRoutes(queue *gin.RouterGroup, svc *service.MediaProbeCampaignService) {
	group := queue.Group("/campaign")
	group.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Next() })
	group.GET("/account", func(c *gin.Context) {
		view, err := svc.Account(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, view)
	})
	group.POST("/account/:action", func(c *gin.Context) {
		var req struct {
			SessionID string `json:"session_id"`
			Confirmed bool   `json:"confirmed"`
			Cookie    string `json:"cookie"`
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 20<<10)
		if !bindProbeExchange(c, &req) {
			return
		}
		view, err := svc.AccountAction(c.Request.Context(), c.Param("action"), req.SessionID, req.Confirmed, req.Cookie)
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, view)
	})
	group.GET("", func(c *gin.Context) {
		view, err := svc.Snapshot(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, view)
	})
	group.POST("/plan", func(c *gin.Context) {
		var req struct {
			LibraryIDs []string `json:"library_ids"`
			MaxFiles   int      `json:"max_files"`
		}
		if !bindProbeExchange(c, &req) {
			return
		}
		plan, err := svc.Plan(c.Request.Context(), req.LibraryIDs, req.MaxFiles)
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, plan)
	})
	group.POST("/start", func(c *gin.Context) {
		var req struct {
			ID        string `json:"id"`
			Digest    string `json:"digest"`
			Confirmed bool   `json:"confirmed"`
		}
		if !bindProbeExchange(c, &req) {
			return
		}
		if err := svc.Start(c.Request.Context(), req.ID, req.Digest, req.Confirmed); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusAccepted)
	})
	group.POST("/pause", func(c *gin.Context) { svc.Pause(); c.Status(http.StatusAccepted) })
}
