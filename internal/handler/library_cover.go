package handler

import (
	"context"
	"errors"
	"fmt"
	"image"
	"net/http"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/service"
	"github.com/gin-gonic/gin"
)

type libraryCoverRequest struct {
	MediaIDs []string `json:"media_ids" binding:"required"`
}

func getLibraryCoverHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		lib, err := svc.Repo.Library.FindByID(c.Request.Context(), c.Param("id"))
		if err != nil {
			writeInternalOrCanceled(c, err)
			return
		}
		if lib == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "媒体库不存在"})
			return
		}
		ids := lib.CoverMediaIDs
		if ids == nil {
			ids = []string{}
		}
		_, items, err := svc.Emby.LibraryCoverSelection(c.Request.Context(), lib.ID, ids)
		response := gin.H{"media_ids": ids, "items": items}
		if errors.Is(err, service.ErrLibraryCoverSelection) {
			response["selection_error"] = err.Error()
		} else if err != nil {
			writeInternalOrCanceled(c, err)
			return
		}
		c.JSON(http.StatusOK, response)
	}
}

func saveLibraryCoverHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req libraryCoverRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请提供 media_ids 数组；空数组表示自动封面"})
			return
		}
		if err := svc.Emby.SaveLibraryCoverSelection(c.Request.Context(), c.Param("id"), req.MediaIDs); err != nil {
			writeLibraryCoverError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"media_ids": req.MediaIDs})
	}
}

func previewLibraryCoverHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req libraryCoverRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请提供 media_ids 数组"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
		defer cancel()
		artworks, _, err := svc.Emby.LibraryCoverSelection(ctx, c.Param("id"), req.MediaIDs)
		if err != nil {
			writeLibraryCoverError(c, err)
			return
		}
		if len(req.MediaIDs) == 0 {
			artworks, err = svc.Emby.AutomaticFolderCoverArtwork(ctx, c.Param("id"), "Primary", 4)
		}
		if err != nil {
			writeLibraryCoverError(c, err)
			return
		}
		body, err := renderLibraryCover(ctx, svc, artworks, 960, 540)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "image/png", body)
	}
}

func libraryCoverImageHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
		defer cancel()
		lib, err := svc.Repo.Library.FindByID(ctx, c.Param("id"))
		if err != nil {
			writeInternalOrCanceled(c, err)
			return
		}
		if lib == nil || !service.LibraryVisibleForUser(ctx, svc.Repo, *lib, mediaVisibilityForRequest(c, svc)) {
			c.JSON(http.StatusNotFound, gin.H{"error": "媒体库不存在"})
			return
		}
		artworks, err := svc.Emby.FolderCoverArtwork(ctx, lib.ID, "Primary", 4)
		if err != nil {
			writeLibraryCoverError(c, err)
			return
		}
		width, height := embyFolderCoverDimensions(c)
		body, err := renderLibraryCover(ctx, svc, artworks, width, height)
		if err != nil {
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.Header("Cache-Control", "private, no-cache")
		c.Data(http.StatusOK, "image/png", body)
	}
}

func renderLibraryCover(ctx context.Context, svc *service.Container, artworks []service.EmbyFolderCoverArtwork, width, height int) ([]byte, error) {
	if len(artworks) == 0 {
		return nil, fmt.Errorf("媒体库没有可用于封面的作品海报")
	}
	images := make([]image.Image, 0, len(artworks))
	for _, art := range artworks {
		img, err := fetchEmbyFolderArtwork(ctx, svc, art.URL)
		if err != nil {
			return nil, fmt.Errorf("作品海报加载失败：%w", err)
		}
		images = append(images, img)
	}
	return buildEmbyFolderCoverGallery(images, width, height)
}

func writeLibraryCoverError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrLibraryCoverSelection) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	writeInternalOrCanceled(c, err)
}
