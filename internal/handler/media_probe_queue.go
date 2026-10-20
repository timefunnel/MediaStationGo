package handler

import (
	"net/http"
	"strconv"

	"github.com/ShukeBta/MediaStationGo/internal/service"
	"github.com/gin-gonic/gin"
)

func registerMediaProbeQueueRoutes(admin *gin.RouterGroup, svc *service.Container) {
	queue := admin.Group("/media-probe-queue")
	queue.Use(func(c *gin.Context) {
		if svc == nil || svc.MediaProbeQueue == nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "文件信息队列不可用"})
			return
		}
		c.Next()
	})
	queue.GET("", func(c *gin.Context) {
		page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "页码无效"})
			return
		}
		result, err := svc.MediaProbeQueue.Snapshot(c.Request.Context(), c.Query("status"), page)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
	})
	queue.PUT("/config", func(c *gin.Context) {
		var cfg service.MediaProbeQueueConfig
		if err := c.ShouldBindJSON(&cfg); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := svc.MediaProbeQueue.SaveConfig(c.Request.Context(), cfg); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	})
	queue.POST("/collect", func(c *gin.Context) {
		count, err := svc.MediaProbeQueue.Collect(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"added": count})
	})
	queue.POST("/plan", func(c *gin.Context) {
		plan, err := svc.MediaProbeQueue.CreatePlan(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, plan)
	})
	queue.POST("/start", func(c *gin.Context) {
		var req struct {
			PlanID    string `json:"plan_id"`
			Confirmed bool   `json:"confirmed"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := svc.MediaProbeQueue.Start(c.Request.Context(), req.PlanID, req.Confirmed); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusAccepted)
	})
	queue.POST("/pause", func(c *gin.Context) { svc.MediaProbeQueue.Pause(); c.Status(http.StatusAccepted) })
	queue.POST("/:media_id/:action", func(c *gin.Context) {
		if err := svc.MediaProbeQueue.UpdateJob(c.Request.Context(), c.Param("media_id"), c.Param("action")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	})
}
