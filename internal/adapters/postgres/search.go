package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

var ErrInvalidSearchPage = errors.New("invalid memory search request")

const (
	DefaultSearchPageSize = 20
	MaxSearchPageSize     = 100
	MaxSearchPage         = 100000
	MaxSearchQueryLength  = 4096
	MaxSearchTerms        = 32
	MaxSearchTermRunes    = 64
	// MaxCJKSearchGrams bounds the overlapping bigrams generated per CJK term so
	// the containment predicate stays small even for long unsegmented runs.
	MaxCJKSearchGrams = 8
)

type SearchRepository struct {
	router *tenantdb.Router
}

func NewSearchRepository(router *tenantdb.Router) *SearchRepository {
	return &SearchRepository{router: router}
}

// term is one retrieval keyword of a query. Latin terms match through the
// full-text lexeme channel; CJK terms cannot be lexemized by the 'simple'
// parser (it folds a whole CJK run into one lexeme), so they match through
// substring containment of their overlapping bigrams instead.
type term struct {
	text string
	cjk  bool
}

// splitSearchTerms tokenizes a query into retrieval terms: runs of letters and
// digits, trimmed of punctuation, de-duplicated, capped to keep the generated
// predicate bounded.
func splitSearchTerms(query string) []term {
	seen := make(map[string]bool)
	terms := make([]term, 0, 8)
	for _, field := range strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		runes := []rune(field)
		if len(runes) > MaxSearchTermRunes {
			runes = runes[:MaxSearchTermRunes]
		}
		text := string(runes)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		terms = append(terms, term{text: text, cjk: containsCJK(text)})
		if len(terms) == MaxSearchTerms {
			break
		}
	}
	return terms
}

func containsCJK(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}

// cjkBigrams returns up to limit distinct overlapping two-rune shingles of a CJK
// run. The 'simple' full-text parser folds a whole CJK run into one lexeme, so a
// query whose wording differs from the stored text would otherwise only match as
// a whole-string substring. Bigrams add partial-overlap recall; the score is the
// fraction of query bigrams present, so closer wording still ranks higher.
func cjkBigrams(text string, limit int) []string {
	runes := []rune(text)
	if len(runes) < 2 || limit < 1 {
		return nil
	}
	grams := make([]string, 0, limit)
	seen := make(map[string]bool, limit)
	for i := 0; i+1 < len(runes) && len(grams) < limit; i++ {
		gram := string(runes[i : i+2])
		if seen[gram] {
			continue
		}
		seen[gram] = true
		grams = append(grams, gram)
	}
	return grams
}

// Search always restricts results to the user's tenant-local User Global Memory
// and, when supplied, their current Session Memory before applying query terms.
// A query with several terms matches a memory through ANY term (OR), ranked by
// how many terms hit; CJK terms match through substring containment because
// the 'simple' parser cannot lexemize them.
func (r *SearchRepository) Search(ctx context.Context, tenantID, userID, sessionID string, request ports.MemorySearchRequest) (ports.MemorySearchPage, error) {
	if tenantID == "" || userID == "" || len(request.Query) > MaxSearchQueryLength || request.Page < 0 || request.Page > MaxSearchPage || request.PageSize < 0 {
		return ports.MemorySearchPage{}, ErrInvalidSearchPage
	}
	if request.Page == 0 {
		request.Page = 1
	}
	if request.PageSize == 0 {
		request.PageSize = DefaultSearchPageSize
	}
	if request.PageSize > MaxSearchPageSize {
		return ports.MemorySearchPage{}, ErrInvalidSearchPage
	}
	offset := int64(request.Page-1) * int64(request.PageSize)
	query := strings.TrimSpace(request.Query)
	terms := splitSearchTerms(query)
	var result ports.MemorySearchPage
	result.Page, result.PageSize = request.Page, request.PageSize

	// matchPredicate builds the per-term match SQL and its bind values. Each
	// term contributes one channel: full-text lexemes for Latin terms,
	// substring containment for CJK terms. Placeholders start at $4 ($1-$3
	// are user/session/memory-type filters).
	matchParts := make([]string, 0, len(terms))
	scoreParts := make([]string, 0, len(terms))
	args := []any{userID, sessionID, request.MemoryType}
	if len(terms) == 0 && query != "" {
		// No letter/digit terms (pure punctuation or symbols): fall back to
		// exact substring containment of the raw query.
		matchParts = append(matchParts, fmt.Sprintf("position($%d in coalesce(content_text, '')) > 0", len(args)+1))
		scoreParts = append(scoreParts, fmt.Sprintf("(CASE WHEN position($%d in coalesce(content_text, '')) > 0 THEN 0.6::real ELSE 0 END)", len(args)))
		args = append(args, query)
	}
	for _, t := range terms {
		if t.cjk {
			grams := cjkBigrams(t.text, MaxCJKSearchGrams)
			if len(grams) <= 1 {
				// Short run: exact containment, no partial-overlap signal.
				matchParts = append(matchParts, fmt.Sprintf("position($%d in coalesce(content_text, '')) > 0", len(args)+1))
				scoreParts = append(scoreParts, fmt.Sprintf("(CASE WHEN position($%d in coalesce(content_text, '')) > 0 THEN 0.6::real ELSE 0 END)", len(args)))
				args = append(args, t.text)
				continue
			}
			hits := make([]string, 0, len(grams))
			for _, gram := range grams {
				hits = append(hits, fmt.Sprintf("(CASE WHEN position($%d in coalesce(content_text, '')) > 0 THEN 1 ELSE 0 END)", len(args)+1))
				args = append(args, gram)
			}
			hitCount := "(" + strings.Join(hits, " + ") + ")"
			matchParts = append(matchParts, hitCount+" > 0")
			scoreParts = append(scoreParts, fmt.Sprintf("(0.6::real * %s / %d)", hitCount, len(grams)))
			continue
		}
		// Latin terms match lexemes with a prefix so a query word also hits its
		// inflections and compounds ("tailwind" -> "tailwindcss"). The term is
		// restricted to letters and digits, so appending the tsquery prefix
		// operator cannot inject query syntax.
		matchParts = append(matchParts, fmt.Sprintf("to_tsvector('simple', coalesce(content_text, '')) @@ to_tsquery('simple', $%d)", len(args)+1))
		scoreParts = append(scoreParts, fmt.Sprintf("(CASE WHEN to_tsvector('simple', coalesce(content_text, '')) @@ to_tsquery('simple', $%d) THEN 1.0::real ELSE 0 END)", len(args)))
		args = append(args, t.text+":*")
	}
	matchSQL := ""
	if len(matchParts) > 0 {
		matchSQL = " AND (" + strings.Join(matchParts, " OR ") + ")"
	}
	scoreSQL := "0::real"
	if len(scoreParts) > 0 {
		scoreSQL = "(" + strings.Join(scoreParts, " + ") + ")"
	}

	filters := `user_id = $1::uuid
		AND ((scope_type = 'user-global' AND scope_id = $1::uuid)
			OR (scope_type = 'session' AND session_id = NULLIF($2, '')::uuid))
		AND ($2 = '' OR EXISTS (SELECT 1 FROM sessions AS requested_session WHERE requested_session.id = NULLIF($2, '')::uuid AND requested_session.user_id = $1::uuid))
		AND status IN ('active', 'stable')
		AND default_retrieval = true
		AND deleted_at IS NULL
		AND (expires_at IS NULL OR expires_at > now())
		AND ($3 = '' OR memory_type = $3)` + matchSQL

	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM memory_nodes WHERE `+filters, args...).
			Scan(&result.Total); err != nil {
			return fmt.Errorf("count memory search results: %w", err)
		}
		selectQuery := `SELECT ` + memoryNodeColumns + `, ` + scoreSQL + ` AS score
			FROM memory_nodes WHERE ` + filters + `
			ORDER BY score DESC, confidence DESC, updated_at DESC, id
			LIMIT $` + fmt.Sprint(len(args)+1) + ` OFFSET $` + fmt.Sprint(len(args)+2)
		selectArgs := append(append([]any{}, args...), request.PageSize, offset)
		rows, err := tx.QueryContext(ctx, selectQuery, selectArgs...)
		if err != nil {
			return fmt.Errorf("search memory nodes: %w", err)
		}
		defer rows.Close()
		result.Items = make([]ports.MemorySearchResult, 0, request.PageSize)
		for rows.Next() {
			var item ports.MemorySearchResult
			item.Node, item.Score, err = scanMemoryWithScore(rows)
			if err != nil {
				return fmt.Errorf("scan memory search result: %w", err)
			}
			result.Items = append(result.Items, item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate memory search results: %w", err)
		}
		return nil
	})
	if err != nil {
		return ports.MemorySearchPage{}, err
	}
	return result, nil
}
func scanMemoryWithScore(row rowScanner) (ports.MemoryNodeRecord, float64, error) {
	var node ports.MemoryNodeRecord
	var sessionID, parentID sql.NullString
	var expiresAt, deletedAt sql.NullTime
	var applicability, content, provenance []byte
	var score float64
	err := row.Scan(
		&node.ID, &node.IdempotencyKey, &node.UserID, &sessionID, &node.ScopeType, &node.ScopeID,
		&parentID, &node.MemoryType, &node.Status, &node.Visibility, &node.Confidence,
		&applicability, &content, &node.ContentText, &node.DefaultRetrieval, &node.Version,
		&node.CreatedAt, &node.UpdatedAt, &provenance, &expiresAt, &deletedAt, &score,
	)
	if err != nil {
		return ports.MemoryNodeRecord{}, 0, err
	}
	if sessionID.Valid {
		node.SessionID = sessionID.String
	}
	if parentID.Valid {
		node.ParentID = parentID.String
	}
	node.Applicability = append(node.Applicability[:0], applicability...)
	node.Content = append(node.Content[:0], content...)
	node.Provenance = append(node.Provenance[:0], provenance...)
	if expiresAt.Valid {
		node.ExpiresAt = &expiresAt.Time
	}
	if deletedAt.Valid {
		node.DeletedAt = &deletedAt.Time
	}
	return node, score, nil
}
