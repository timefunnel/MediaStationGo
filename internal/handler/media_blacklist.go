package handler

import (
	"errors"
	"net/http"

	"github.com/ShukeBta/MediaStationGo/internal/middleware"
	"github.com/ShukeBta/MediaStationGo/internal/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func mediaBlacklistError(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

func setMediaBlacklistHandler(svc *service.Container, blocked bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := c.GetString(middleware.CtxUserID)
		if err := svc.Emby.SetMediaBlacklisted(c.Request.Context(), uid, c.Param("id"), blocked); err != nil {
			mediaBlacklistError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"blocked": blocked})
	}
}

func mediaBlacklistStatusHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		blocked, err := svc.Emby.MediaBlacklisted(c.Request.Context(), c.GetString(middleware.CtxUserID), c.Param("id"))
		if err != nil {
			mediaBlacklistError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"blocked": blocked})
	}
}

func listMediaBlacklistHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		items, err := svc.Emby.ListMediaBlacklist(c.Request.Context(), c.GetString(middleware.CtxUserID))
		if err != nil {
			mediaBlacklistError(c, err)
			return
		}
		for i := range items {
			if items[i].Media != nil {
				media := mediaForResponse(c, *items[i].Media)
				items[i].Media = &media
			}
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	}
}

func removeBlacklistItemHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.Emby.RemoveBlacklistItem(c.Request.Context(), c.GetString(middleware.CtxUserID), c.Param("id")); err != nil {
			mediaBlacklistError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"blocked": false})
	}
}
