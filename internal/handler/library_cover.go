package handler

import (
	"context"
	"errors"
	"fmt"
	"image"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/service"
	"github.com/gin-gonic/gin"
)

type libraryCoverRequest struct {
	MediaIDs []string `json:"media_ids" binding:"required"`
}

func libraryCoverCandidatesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
		if err != nil || page < 1 || page > 10000000 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid page"})
			return
		}
		ctx := c.Request.Context()
		lib, err := svc.Repo.Library.FindByID(ctx, c.Param("id"))
		if err != nil {
			writeInternalOrCanceled(c, err)
			return
		}
		visibility := mediaVisibilityForRequest(c, svc)
		if lib == nil || !service.LibraryVisibleForUser(ctx, svc.Repo, *lib, visibility) {
			c.JSON(http.StatusNotFound, gin.H{"error": "媒体库不存在"})
			return
		}
		result, err := svc.Media.BrowseLibrary(ctx, lib.ID, service.LibraryBrowseOptions{Page: page, Query: strings.TrimSpace(c.Query("q"))}, visibility)
		if err != nil {
			writeInternalOrCanceled(c, err)
			return
		}
		rows := make([]model.Media, 0, len(result.Items)+len(result.SeriesCards))
		if result.IsSeries {
			for _, card := range result.SeriesCards {
				rows = append(rows, card.Rep)
			}
		} else {
			for _, item := range result.Items {
				rows = append(rows, item.Media)
			}
		}
		items, err := svc.Emby.LibraryCoverCandidates(ctx, lib.Type, rows)
		if err != nil {
			writeInternalOrCanceled(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": items, "total": result.Total, "page": result.Page, "page_size": result.PageSize})
	}
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
		lib, err := svc.Repo.Library.FindByID(ctx, c.Param("id"))
		if err != nil {
			writeLibraryCoverError(c, err)
			return
		}
		if lib == nil {
			writeLibraryCoverError(c, fmt.Errorf("%w: 媒体库不存在", service.ErrLibraryCoverSelection))
			return
		}
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
		body, err := renderLibraryCover(ctx, svc, lib.Name, artworks, 960, 540)
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
		body, err := renderLibraryCover(ctx, svc, lib.Name, artworks, width, height)
		if err != nil {
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.Header("Cache-Control", "private, no-cache")
		c.Data(http.StatusOK, "image/png", body)
	}
}

func renderLibraryCover(ctx context.Context, svc *service.Container, title string, artworks []service.EmbyFolderCoverArtwork, width, height int) ([]byte, error) {
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
	return buildEmbyFolderCoverGallery(images, title, width, height)
}

func writeLibraryCoverError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrLibraryCoverSelection) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	writeInternalOrCanceled(c, err)
}
