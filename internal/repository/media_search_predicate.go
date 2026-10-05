package repository

import (
	"strings"
	"unicode"

	"github.com/mozillazg/go-pinyin"
)

// MediaSearchTermSQL is the shared media/Emby substring predicate. The term is
// normalized by the caller; alias is an internal table name, never user input.
// Plot descriptions are display metadata, not searchable media identity.
func MediaSearchTermSQL(term, alias string) (string, []any) {
	columns := []string{"title", "original_name", "path", "relative_path", "genres", "actors"}
	needsLower, needsAliases := false, true
	for _, r := range term {
		if unicode.SimpleFold(r) != r {
			needsLower = true
		}
		// Use the same dictionary as mediaSearchAliases. A recognized Han rune
		// is always transliterated, so it cannot appear in either alias column.
		// Unknown Han and other scripts retain their existing alias lookup.
		if unicode.Is(unicode.Han, r) {
			if _, known := pinyin.PinyinDict[int(r)]; known {
				needsAliases = false
			}
		}
	}
	if needsAliases {
		columns = append(columns, "search_pinyin", "search_initials")
	}
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	pattern := "%" + escapeLike(term) + "%"
	clauses := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	for _, column := range columns {
		value := "COALESCE(" + prefix + column + ", '')"
		if needsLower {
			value = "LOWER(" + value + ")"
		}
		clauses = append(clauses, value+" LIKE ? ESCAPE '\\'")
		args = append(args, pattern)
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
}
