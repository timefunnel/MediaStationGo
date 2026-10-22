package handler

import (
	"errors"
	"net/http"

	"github.com/ShukeBta/MediaStationGo/internal/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func clientDanmakuHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc.Emby == nil || svc.Danmaku == nil {
			embyError(c, http.StatusServiceUnavailable, "弹幕关联服务不可用")
			return
		}
		requested := c.Query("MediaSourceId")
		if requested == "" {
			requested = c.Param("id")
		}
		mediaID, err := svc.Emby.ResolveMediaSourceID(c.Request.Context(), c.Param("id"), embyUserID(c), requested)
		if err != nil {
			embyError(c, http.StatusBadRequest, err.Error())
			return
		}
		if mediaID == "" {
			embyError(c, http.StatusNotFound, "media not found")
			return
		}
		var out service.ClientDanmakuContext
		if c.Request.Method == http.MethodPut {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
			var in service.ClientDanmakuUpdate
			if err := c.ShouldBindJSON(&in); err != nil {
				embyError(c, http.StatusBadRequest, err.Error())
				return
			}
			out, err = svc.Danmaku.SaveClientAssociation(c.Request.Context(), mediaID, embyUserID(c), in)
		} else {
			out, err = svc.Danmaku.ClientContext(c.Request.Context(), mediaID, embyUserID(c))
		}
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, service.ErrDanmakuInvalidInput) {
				status = http.StatusBadRequest
			}
			if errors.Is(err, service.ErrDanmakuConflict) {
				status = http.StatusConflict
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				status = http.StatusNotFound
			}
			embyError(c, status, err.Error())
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, out)
	}
}

func retiredServerDanmakuHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusGone, gin.H{"code": "danmaku_client_required", "error": "弹幕已迁移至播放器本地获取，请使用支持内置弹幕模块的播放器"})
	}
}
