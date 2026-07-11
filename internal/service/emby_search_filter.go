package service

import (
	"strings"
	"unicode"

	"gorm.io/gorm"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

type embyMediaSearch struct {
	term       string
	terms      []string
	startsWith bool
}

func embySearchFromParams(p ItemsParams) embyMediaSearch {
	if terms := embySearchTerms(p.SearchTerm); len(terms) > 0 {
		return embyMediaSearch{term: strings.Join(terms, " "), terms: terms}
	}
	if term := strings.ToLower(strings.TrimSpace(p.NameStartsWith)); term != "" {
		return embyMediaSearch{term: term, startsWith: true}
	}
	return embyMediaSearch{}
}

func embyHasMediaSearch(p ItemsParams) bool {
	return embySearchFromParams(p).term != ""
}

func applyEmbyMediaSearch(q *gorm.DB, p ItemsParams) *gorm.DB {
	search := embySearchFromParams(p)
	if search.term == "" {
		return q
	}
	if search.startsWith {
		pattern := escapeEmbyLike(search.term) + "%"
		return q.Where("(LOWER(COALESCE(media.title, '')) LIKE ? ESCAPE '\\' OR LOWER(COALESCE(media.original_name, '')) LIKE ? ESCAPE '\\')", pattern, pattern)
	}
	for _, term := range search.terms {
		pattern := "%" + escapeEmbyLike(term) + "%"
		q = q.Where(
			"(LOWER(COALESCE(media.title, '')) LIKE ? ESCAPE '\\' OR LOWER(COALESCE(media.original_name, '')) LIKE ? ESCAPE '\\' OR LOWER(COALESCE(media.path, '')) LIKE ? ESCAPE '\\' OR LOWER(COALESCE(media.relative_path, '')) LIKE ? ESCAPE '\\' OR LOWER(COALESCE(media.overview, '')) LIKE ? ESCAPE '\\' OR LOWER(COALESCE(media.genres, '')) LIKE ? ESCAPE '\\')",
			pattern, pattern, pattern, pattern, pattern, pattern,
		)
	}
	return q
}

func embyMediaMatchesSearch(row model.Media, p ItemsParams) bool {
	search := embySearchFromParams(p)
	if search.term == "" {
		return true
	}
	values := []string{
		strings.ToLower(row.Title),
		strings.ToLower(row.OriginalName),
	}
	if search.startsWith {
		for _, value := range values {
			if strings.HasPrefix(value, search.term) {
				return true
			}
		}
		return false
	}
	values = append(values,
		strings.ToLower(row.Path),
		strings.ToLower(row.RelativePath),
		strings.ToLower(row.Overview),
		strings.ToLower(row.Genres),
	)
	for _, term := range search.terms {
		matched := false
		for _, value := range values {
			if strings.Contains(value, term) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func embySearchTerms(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
	out := make([]string, 0, len(fields))
	seen := map[string]struct{}{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		lower := strings.ToLower(field)
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}
		out = append(out, lower)
	}
	return out
}

func escapeEmbyLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}
