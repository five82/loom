package store

import (
	"context"
	"fmt"
	"os"
)

// Finding is one audit check and what it matched. Checks that matched nothing
// are still reported, so the output says what was looked for instead of leaving
// silence to mean either clean or unchecked.
//
// Every match is named. A cap would have to guess which rows matter, and the
// shape of a finding is usually in the rows it does not show: thirty-four
// unmatched episodes read as one broken show until the list makes it seven.
type Finding struct {
	Check string `json:"check"`
	// Integrity marks a check that must always be zero. The rest describe
	// metadata a provider did not supply, which is a gap rather than a defect.
	Integrity bool     `json:"integrity"`
	Count     int      `json:"count"`
	Matches   []string `json:"matches,omitempty"`
}

type AuditReport struct {
	SchemaVersion int       `json:"schema_version"`
	Findings      []Finding `json:"findings"`
}

// IntegrityProblems totals the rows matched by integrity checks.
func (r AuditReport) IntegrityProblems() int {
	total := 0
	for _, finding := range r.Findings {
		if finding.Integrity {
			total += finding.Count
		}
	}
	return total
}

// auditCheck names one problem and the query that finds it. Every query returns
// a single text column per offending row, so one runner serves them all.
type auditCheck struct {
	name  string
	query string
}

var integrityChecks = []auditCheck{{
	name: "foreign key violations",
	query: `SELECT "table" || ' row ' || IFNULL(rowid, '?') || ' references a missing ' || "parent"
FROM pragma_foreign_key_check`,
}, {
	// Shows and seasons are grouping rows, so only the kinds that are played
	// are expected to carry a file.
	name: "items without a media file",
	query: `SELECT i.id || ' ' || i.title FROM items i
WHERE i.available = 1 AND i.kind IN ('movie', 'episode', 'unmatched')
    AND NOT EXISTS (SELECT 1 FROM media_files m WHERE m.item_id = i.id)
ORDER BY i.id`,
}, {
	name: "media probe failures",
	query: `SELECT m.path || ': ' || m.probe_error FROM media_files m
JOIN items i ON i.id = m.item_id
WHERE i.available = 1 AND m.probe_error <> ''
ORDER BY m.id`,
}, {
	// A probe that succeeded but reported no duration is the quiet version of
	// the same failure: nothing errors, and the file cannot be resumed.
	name: "media without a duration",
	query: `SELECT m.path FROM media_files m
JOIN items i ON i.id = m.item_id
WHERE i.available = 1 AND m.probe_error = '' AND m.duration_ms <= 0
ORDER BY m.id`,
}, {
	// Season posters and episode artwork are resolved by walking up to the
	// show, and image selection refuses a season with no parent.
	name: "items with a broken parent link",
	query: `SELECT i.id || ' ' || i.kind || ' ' || i.title FROM items i
LEFT JOIN items p ON p.id = i.parent_id
WHERE i.available = 1 AND (
    (i.kind IN ('movie', 'show') AND i.parent_id IS NOT NULL)
    OR (i.kind = 'season' AND (p.id IS NULL OR p.kind <> 'show'))
    OR (i.kind = 'episode' AND (p.id IS NULL OR p.kind <> 'season'))
    OR (i.kind = 'unmatched' AND (p.id IS NULL OR p.kind <> 'show')))
ORDER BY i.id`,
}, {
	// Directors are stored for movies only, because TV credits them per
	// episode. A director anywhere else means something wrote credits for a
	// kind that is not supposed to have them.
	name: "director credits outside movies",
	query: `SELECT i.id || ' ' || i.kind || ' ' || i.title FROM item_credits c
JOIN items i ON i.id = c.item_id
WHERE c.role = 'director' AND i.kind <> 'movie'
GROUP BY i.id
ORDER BY i.id`,
}}

var metadataChecks = []auditCheck{{
	name: "items awaiting a metadata match",
	query: `SELECT i.id || ' ' || i.title FROM items i
WHERE i.available = 1 AND (i.kind = 'unmatched'
    OR (i.kind IN ('movie', 'show') AND i.tmdb_id = 0))
ORDER BY i.id`,
}, {
	// Matched but never detailed is the state a scan is meant to clear, so a
	// standing count here means the backfill is not running to completion.
	name: "matches without loaded details",
	query: `SELECT i.id || ' ' || i.title FROM items i
WHERE i.available = 1 AND i.kind IN ('movie', 'show')
    AND i.tmdb_id <> 0 AND i.details_loaded = 0
ORDER BY i.id`,
}, {
	name: "titles without a cast",
	query: `SELECT i.id || ' ' || i.title FROM items i
WHERE i.available = 1 AND i.kind IN ('movie', 'show') AND i.details_loaded = 1
    AND NOT EXISTS (SELECT 1 FROM item_credits c WHERE c.item_id = i.id AND c.role = 'actor')
ORDER BY i.id`,
}, {
	name: "movies without a director",
	query: `SELECT i.id || ' ' || i.title FROM items i
WHERE i.available = 1 AND i.kind = 'movie' AND i.details_loaded = 1
    AND NOT EXISTS (SELECT 1 FROM item_credits c WHERE c.item_id = i.id AND c.role = 'director')
ORDER BY i.id`,
}, {
	name: "movies without genres",
	query: `SELECT i.id || ' ' || i.title FROM items i
WHERE i.available = 1 AND i.kind = 'movie' AND i.details_loaded = 1
    AND NOT EXISTS (SELECT 1 FROM item_genres g WHERE g.item_id = i.id)
ORDER BY i.id`,
}, {
	// An episode under a matched show keeps its filename-derived placeholder
	// title until the season fetch reaches it, which a numbering disagreement
	// with the provider can prevent for good.
	name: "episodes of a matched show without a match",
	// An unmatched episode still carries its filename-derived title, so naming
	// the show is the only way the list says which one is affected.
	query: `SELECT i.id || ' ' || show.title || ' ' ||
    printf('S%02dE%02d', i.season_number, i.episode_number) FROM items i
JOIN items season ON season.id = i.parent_id
JOIN items show ON show.id = season.parent_id
WHERE i.available = 1 AND i.kind = 'episode' AND i.tmdb_id = 0 AND show.tmdb_id <> 0
ORDER BY show.title, i.season_number, i.episode_number`,
}, {
	// Seasons and episodes fall back to the show's artwork, so only the two
	// kinds that have to supply their own are checked.
	name: "titles without a poster",
	query: `SELECT i.id || ' ' || i.title FROM items i
WHERE i.available = 1 AND i.kind IN ('movie', 'show')
    AND NOT EXISTS (SELECT 1 FROM images g WHERE g.item_id = i.id AND g.kind = 'poster')
ORDER BY i.id`,
}}

// Audit reports catalog problems without changing anything. It reads the
// database directly rather than going through the daemon, so it still works
// when the daemon will not start.
func (s *Store) Audit(ctx context.Context) (AuditReport, error) {
	var report AuditReport
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&report.SchemaVersion); err != nil {
		return report, fmt.Errorf("read schema version: %w", err)
	}
	for _, check := range integrityChecks {
		finding, err := s.runAuditCheck(ctx, check, true)
		if err != nil {
			return report, err
		}
		report.Findings = append(report.Findings, finding)
	}
	artwork, err := s.missingArtworkFiles(ctx)
	if err != nil {
		return report, err
	}
	report.Findings = append(report.Findings, artwork)
	for _, check := range metadataChecks {
		finding, err := s.runAuditCheck(ctx, check, false)
		if err != nil {
			return report, err
		}
		report.Findings = append(report.Findings, finding)
	}
	return report, nil
}

func (s *Store) runAuditCheck(ctx context.Context, check auditCheck, integrity bool) (Finding, error) {
	finding := Finding{Check: check.name, Integrity: integrity}
	rows, err := s.db.QueryContext(ctx, check.query)
	if err != nil {
		return finding, fmt.Errorf("audit %s: %w", check.name, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var match string
		if err := rows.Scan(&match); err != nil {
			return finding, fmt.Errorf("scan audit %s: %w", check.name, err)
		}
		finding.Count++
		finding.Matches = append(finding.Matches, match)
	}
	return finding, rows.Err()
}

// missingArtworkFiles is the one check the database cannot answer: an images
// row survives whatever removed the file it points at, and the gap only shows
// up as a broken image in a client.
func (s *Store) missingArtworkFiles(ctx context.Context) (Finding, error) {
	finding := Finding{Check: "artwork files missing from disk", Integrity: true}
	rows, err := s.db.QueryContext(ctx, `
SELECT images.path FROM images JOIN items ON items.id = images.item_id
WHERE items.available = 1
ORDER BY images.id`)
	if err != nil {
		return finding, fmt.Errorf("audit artwork files: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return finding, fmt.Errorf("scan artwork path: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return finding, err
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			continue
		}
		finding.Count++
		finding.Matches = append(finding.Matches, path)
	}
	return finding, nil
}
