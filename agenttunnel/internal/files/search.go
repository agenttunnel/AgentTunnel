package files

import (
	"context"
	"path"
	"sort"
	"strings"
)

// MaxSearchResults caps how many matches a search returns, so the composer's
// @ menu stays cheap to render and to send over the socket.
const MaxSearchResults = 50

// Match is one file or folder whose name matches a search.
type Match struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
}

// Search fuzzy-matches query against the names of the workdir's files and
// folders and returns the best `limit` hits. The candidate list is the same
// one Tree uses, so .gitignore is honoured inside a repo and heavy directories
// are skipped outside one. An empty query returns the shallowest entries.
func Search(ctx context.Context, workdir, query string, limit int) ([]Match, error) {
	if limit <= 0 || limit > MaxSearchResults {
		limit = MaxSearchResults
	}
	paths, err := trackedPaths(ctx, workdir)
	if err != nil {
		return nil, err
	}
	dirs := map[string]bool{}
	for _, p := range paths {
		for d := path.Dir(p); d != "." && d != "/" && !dirs[d]; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	q := strings.ToLower(strings.TrimSpace(query))

	var best []scored
	consider := func(m Match) {
		s, ok := matchScore(q, m.Path)
		if !ok {
			return
		}
		c := scored{Match: m, score: s}
		if len(best) == limit && !c.beats(best[len(best)-1]) {
			return
		}
		best = append(best, c)
		sort.SliceStable(best, func(i, j int) bool { return best[i].beats(best[j]) })
		if len(best) > limit {
			best = best[:limit]
		}
	}
	for _, p := range paths {
		consider(Match{Path: p})
	}
	for d := range dirs {
		consider(Match{Path: d, Dir: true})
	}

	out := make([]Match, len(best))
	for i, b := range best {
		out[i] = b.Match
	}
	return out, nil
}

type scored struct {
	Match
	score int
}

// beats orders results: higher score first, then shallower paths, folders
// before files at equal depth, then alphabetical.
func (a scored) beats(b scored) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	da, db := strings.Count(a.Path, "/"), strings.Count(b.Path, "/")
	if da != db {
		return da < db
	}
	if a.Dir != b.Dir {
		return a.Dir
	}
	return a.Path < b.Path
}

// matchScore scores a lowercased query against a path. A match in the base
// name outranks one that only appears in a parent folder. ok is false when the
// query's runes do not all appear in order.
func matchScore(query, p string) (int, bool) {
	if query == "" {
		return 0, true
	}
	lower := strings.ToLower(p)
	if s, ok := fuzzyScore(query, path.Base(lower)); ok {
		return s * 2, true
	}
	return fuzzyScore(query, lower)
}

// fuzzyScore reports whether every byte of query appears in text in order and
// how tightly: matches at word starts and runs of consecutive matches score
// higher. Byte-wise matching is enough because UTF-8 continuation bytes never
// collide with ASCII separators.
func fuzzyScore(query, text string) (int, bool) {
	qi, score, prev := 0, 0, -2
	for i := 0; i < len(text) && qi < len(query); i++ {
		if text[i] != query[qi] {
			continue
		}
		s := 1
		if i == 0 || isWordStart(text[i-1]) {
			s += 3
		}
		if i == prev+1 {
			s += 4
		}
		score += s
		prev = i
		qi++
	}
	return score, qi == len(query)
}

func isWordStart(c byte) bool {
	switch c {
	case '/', '.', '-', '_', ' ':
		return true
	}
	return false
}
