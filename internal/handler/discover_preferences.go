package handler

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/service"
)

type discoverPreferenceInput struct {
	SelectedSections *[]string `json:"selected_sections" binding:"required"`
}

func getDiscoverPreferenceHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		row, err := svc.Repo.DiscoverPreference.FindByUserID(c.Request.Context(), currentUserID(c))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if row == nil {
			c.JSON(http.StatusOK, gin.H{"configured": false, "selected_sections": []string{}})
			return
		}
		selected, _ := normalizeDiscoverPreferenceSections(
			row.SelectedSections,
			enabledDiscoverSections(c.Request.Context(), svc),
			discoverAdultAllowed(c, svc),
			false,
		)
		c.JSON(http.StatusOK, gin.H{"configured": true, "selected_sections": selected})
	}
}

func updateDiscoverPreferenceHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input discoverPreferenceInput
		if err := c.ShouldBindJSON(&input); err != nil || input.SelectedSections == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "selected_sections is required"})
			return
		}
		selected, err := normalizeDiscoverPreferenceSections(
			*input.SelectedSections,
			enabledDiscoverSections(c.Request.Context(), svc),
			discoverAdultAllowed(c, svc),
			true,
		)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		row := &model.UserDiscoverPreference{UserID: currentUserID(c), SelectedSections: selected}
		if err := svc.Repo.DiscoverPreference.Upsert(c.Request.Context(), row); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"configured": true, "selected_sections": selected})
	}
}

func normalizeDiscoverPreferenceSections(
	raw []string,
	sections []discoverSectionDef,
	adultAllowed bool,
	rejectUnknown bool,
) ([]string, error) {
	if len(raw) > len(discoverSectionCatalog)+8 {
		return nil, fmt.Errorf("too many discover sections")
	}
	allowed := make(map[string]struct{}, len(sections))
	order := make([]string, 0, len(sections))
	for _, section := range sections {
		if section.Group == "adult" && !adultAllowed {
			continue
		}
		allowed[section.Key] = struct{}{}
		order = append(order, section.Key)
	}
	selected := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		key := strings.TrimSpace(value)
		if key == "adult_javdb_performers" {
			key = "adult_javdb_performers_monthly"
		}
		if key == "" {
			if rejectUnknown {
				return nil, fmt.Errorf("discover section key is empty")
			}
			continue
		}
		if _, ok := allowed[key]; !ok {
			if rejectUnknown {
				return nil, fmt.Errorf("unsupported discover section: %s", key)
			}
			continue
		}
		selected[key] = struct{}{}
	}
	out := make([]string, 0, len(selected))
	for _, key := range order {
		if _, ok := selected[key]; ok {
			out = append(out, key)
		}
	}
	return out, nil
}
