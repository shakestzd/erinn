package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/models"
)

const (
	adherenceOverrideThreshold = 1
)

var (
	portSourcePrefixes = []string{
		"packages/plugin-core/",
		"plugin/commands/",
		"plugin/agents/",
		"plugin/skills/",
		"plugin/templates/",
		"plugin/static/",
		"plugin/config/",
	}
	portGeneratedPrefixes = []string{
		"port/packages/codex-marketplace/",
		"plugin/.claude-plugin/",
		"plugin/hooks/",
	}
)

func LoadSessionAdherenceNodes(wipnoteDir string) ([]*models.Node, error) {
	if strings.TrimSpace(wipnoteDir) == "" {
		return nil, nil
	}
	nodes, err := graph.LoadAll(wipnoteDir)
	if err != nil {
		return nil, fmt.Errorf("load adherence nodes: %w", err)
	}
	return nodes, nil
}

func DeriveSessionAdherence(database *sql.DB, sessionID string, nodes []*models.Node) (*models.SessionAdherence, error) {
	if database == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}

	closedItemIDs := closedItemsForSession(sessionID, nodes)
	commitSet, err := commitFeatureSet(database, sessionID)
	if err != nil {
		return nil, err
	}
	gateRecord, err := LatestGateRecordForSession(database, sessionID)
	if err != nil {
		return nil, err
	}
	sessionFiles, err := featureFilesForSession(database, sessionID)
	if err != nil {
		return nil, err
	}
	duplicateLinked, duplicateTargets := duplicateLinksForSession(sessionID, nodes)
	acceptedAdvisoryCount := acceptedAdvisoryCountForSession(sessionID, nodes)
	allowlistHits := 0
	if gateRecord != nil {
		allowlistHits = gateRecord.AllowlistHitCount
	}

	checks := []models.SessionAdherenceCheck{
		commitsClosedCheck(closedItemIDs, commitSet),
		gateRunCheck(gateRecord),
		portRegenCheck(sessionFiles),
		duplicateLinksCheck(duplicateLinked, duplicateTargets),
		overrideAccumulationCheck(acceptedAdvisoryCount, allowlistHits),
	}

	applicable := 0
	passed := 0
	warned := 0
	failed := 0
	scoreTotal := 0.0
	for _, check := range checks {
		switch check.Status {
		case models.SessionAdherenceNA:
			continue
		case models.SessionAdherencePass:
			applicable++
			passed++
			scoreTotal += 1
		case models.SessionAdherenceWarn:
			applicable++
			warned++
			scoreTotal += 0.5
		case models.SessionAdherenceFail:
			applicable++
			failed++
		}
	}

	score := 100
	if applicable > 0 {
		score = int((scoreTotal / float64(applicable) * 100) + 0.5)
	}

	return &models.SessionAdherence{
		Score:      score,
		Applicable: applicable,
		Passed:     passed,
		Warned:     warned,
		Failed:     failed,
		Checks:     checks,
	}, nil
}

func ListSessionAdherenceTrend(database *sql.DB, projectDir string, nodes []*models.Node, limit int) ([]models.SessionAdherenceTrendPoint, error) {
	if database == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := database.Query(`
		SELECT session_id, agent_assigned, created_at, COALESCE(completed_at, '')
		FROM sessions
		WHERE project_dir = ?
		  AND is_subagent = FALSE
		  AND status = 'completed'
		ORDER BY created_at DESC
		LIMIT ?`, projectDir, limit)
	if err != nil {
		return nil, fmt.Errorf("list adherence trend sessions: %w", err)
	}
	defer rows.Close()

	var points []models.SessionAdherenceTrendPoint
	for rows.Next() {
		var (
			sessionID    string
			harness      string
			createdRaw   string
			completedRaw string
		)
		if err := rows.Scan(&sessionID, &harness, &createdRaw, &completedRaw); err != nil {
			return nil, fmt.Errorf("scan adherence trend row: %w", err)
		}
		adherence, err := DeriveSessionAdherence(database, sessionID, nodes)
		if err != nil {
			return nil, err
		}
		createdAt, _ := parseRFC3339ish(createdRaw)
		completedAt, _ := parseRFC3339ish(completedRaw)
		point := models.SessionAdherenceTrendPoint{
			SessionID: sessionID,
			Harness:   harness,
			CreatedAt: createdAt,
			Score:     0,
		}
		if !completedAt.IsZero() {
			point.CompletedAt = completedAt
		}
		if adherence != nil {
			point.Score = adherence.Score
			point.Passed = adherence.Passed
			point.Warned = adherence.Warned
			point.Failed = adherence.Failed
		}
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate adherence trend rows: %w", err)
	}
	slices.Reverse(points)
	return points, nil
}

func closedItemsForSession(sessionID string, nodes []*models.Node) []string {
	var out []string
	for _, node := range nodes {
		if node == nil || node.ClaimedBySession != sessionID {
			continue
		}
		if node.Status == models.StatusDone || node.Status == models.StatusEnded {
			out = append(out, node.ID)
		}
	}
	slices.Sort(out)
	return out
}

func duplicateLinksForSession(sessionID string, nodes []*models.Node) (int, []string) {
	targets := make(map[string]struct{})
	count := 0
	for _, node := range nodes {
		if node == nil || node.ClaimedBySession != sessionID {
			continue
		}
		for _, edge := range node.Edges[string(models.RelRelatesTo)] {
			if strings.HasPrefix(edge.Title, "needs-triage-dup") || edge.Properties["tag"] == "needs-triage-dup" {
				count++
				if edge.TargetID != "" {
					targets[edge.TargetID] = struct{}{}
				}
			}
		}
	}
	out := make([]string, 0, len(targets))
	for target := range targets {
		out = append(out, target)
	}
	slices.Sort(out)
	return count, out
}

func acceptedAdvisoryCountForSession(sessionID string, nodes []*models.Node) int {
	count := 0
	for _, node := range nodes {
		if node == nil || node.ClaimedBySession != sessionID {
			continue
		}
		if acceptedAdvisoryOf(node) != "" {
			count++
		}
	}
	return count
}

func acceptedAdvisoryOf(n *models.Node) string {
	if n == nil || n.Content == "" {
		return ""
	}
	const marker = "accepted-advisory (provenance override): "
	for _, line := range strings.Split(n.Content, "\n") {
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, marker); idx >= 0 {
			return strings.TrimSpace(line[idx+len(marker):])
		}
	}
	return ""
}

func commitFeatureSet(database *sql.DB, sessionID string) (map[string]struct{}, error) {
	rows, err := database.Query(`
		SELECT DISTINCT feature_id
		FROM git_commits
		WHERE session_id = ?
		  AND feature_id IS NOT NULL
		  AND feature_id != ''`, sessionID)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return map[string]struct{}{}, nil
		}
		return nil, fmt.Errorf("query session commits: %w", err)
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan session commit feature: %w", err)
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

func featureFilesForSession(database *sql.DB, sessionID string) ([]string, error) {
	rows, err := database.Query(`
		SELECT DISTINCT file_path
		FROM feature_files
		WHERE session_id = ?
		  AND file_path IS NOT NULL
		  AND file_path != ''`, sessionID)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("query session feature files: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var filePath string
		if err := rows.Scan(&filePath); err != nil {
			return nil, fmt.Errorf("scan session feature file: %w", err)
		}
		out = append(out, filepath.ToSlash(filePath))
	}
	slices.Sort(out)
	return out, rows.Err()
}

func commitsClosedCheck(closedItemIDs []string, commitSet map[string]struct{}) models.SessionAdherenceCheck {
	check := models.SessionAdherenceCheck{
		Key:   "commits_closed",
		Label: "Committed what it closed",
	}
	if len(closedItemIDs) == 0 {
		check.Status = models.SessionAdherenceNA
		check.Summary = "No closed work items attributed to this session."
		return check
	}

	var missing []string
	for _, id := range closedItemIDs {
		if _, ok := commitSet[id]; !ok {
			missing = append(missing, id)
		}
	}
	check.Count = len(closedItemIDs)
	if len(missing) == 0 {
		check.Status = models.SessionAdherencePass
		check.Summary = fmt.Sprintf("%d/%d closed item(s) have linked commits.", len(closedItemIDs), len(closedItemIDs))
		check.Items = closedItemIDs
		return check
	}
	check.Status = models.SessionAdherenceFail
	check.Summary = fmt.Sprintf("%d/%d closed item(s) are missing linked commits.", len(missing), len(closedItemIDs))
	check.Items = missing
	return check
}

func gateRunCheck(gateRecord *GateRecord) models.SessionAdherenceCheck {
	check := models.SessionAdherenceCheck{
		Key:   "gate_ran",
		Label: "Ran the gate",
	}
	if gateRecord == nil {
		check.Status = models.SessionAdherenceFail
		check.Summary = "No session-local gate record found."
		return check
	}
	if gateRecord.Status == "pass" {
		check.Status = models.SessionAdherencePass
		check.Summary = "Latest gate record passed."
		if gateRecord.AllowlistHitCount > 0 {
			check.Summary = fmt.Sprintf("Latest gate record passed with %d allowlist hit(s).", gateRecord.AllowlistHitCount)
		}
		return check
	}
	check.Status = models.SessionAdherenceFail
	check.Summary = fmt.Sprintf("Latest gate record status: %s.", gateRecord.Status)
	return check
}

func portRegenCheck(sessionFiles []string) models.SessionAdherenceCheck {
	check := models.SessionAdherenceCheck{
		Key:   "port_regen",
		Label: "Regenerated ports",
	}
	sourceTouched := false
	generatedTouched := false
	for _, filePath := range sessionFiles {
		if matchesAnyPrefix(filePath, portSourcePrefixes) {
			sourceTouched = true
		}
		if matchesAnyPrefix(filePath, portGeneratedPrefixes) {
			generatedTouched = true
		}
	}
	if !sourceTouched {
		check.Status = models.SessionAdherenceNA
		check.Summary = "No plugin generator source files were committed in this session."
		return check
	}
	if generatedTouched {
		check.Status = models.SessionAdherencePass
		check.Summary = "Committed both plugin generator sources and regenerated target trees."
		return check
	}
	check.Status = models.SessionAdherenceFail
	check.Summary = "Committed plugin generator sources without regenerated target tree evidence."
	return check
}

func duplicateLinksCheck(linkCount int, targets []string) models.SessionAdherenceCheck {
	check := models.SessionAdherenceCheck{
		Key:   "duplicate_links",
		Label: "Linked duplicates",
	}
	if linkCount == 0 {
		check.Status = models.SessionAdherenceNA
		check.Summary = "No duplicate-link marker was attributed to this session."
		return check
	}
	check.Status = models.SessionAdherencePass
	check.Count = linkCount
	check.Items = targets
	check.Summary = fmt.Sprintf("%d duplicate link marker(s) recorded.", linkCount)
	return check
}

func overrideAccumulationCheck(acceptedAdvisories, allowlistHits int) models.SessionAdherenceCheck {
	check := models.SessionAdherenceCheck{
		Key:   "override_accumulation",
		Label: "Override accumulation",
		Count: acceptedAdvisories + allowlistHits,
	}
	switch {
	case acceptedAdvisories > adherenceOverrideThreshold && allowlistHits > adherenceOverrideThreshold:
		check.Status = models.SessionAdherenceWarn
		check.Summary = fmt.Sprintf("Accepted-advisory used %d times and gate allowlist hit %d times.", acceptedAdvisories, allowlistHits)
	case acceptedAdvisories > adherenceOverrideThreshold:
		check.Status = models.SessionAdherenceWarn
		check.Summary = fmt.Sprintf("Accepted-advisory used %d times in one session.", acceptedAdvisories)
	case allowlistHits > adherenceOverrideThreshold:
		check.Status = models.SessionAdherenceWarn
		check.Summary = fmt.Sprintf("Gate allowlist hit %d times in one session.", allowlistHits)
	default:
		check.Status = models.SessionAdherencePass
		check.Summary = fmt.Sprintf("Accepted-advisory: %d, gate allowlist hits: %d.", acceptedAdvisories, allowlistHits)
	}
	return check
}

func matchesAnyPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func parseRFC3339ish(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	layouts := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"}
	for _, layout := range layouts {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported time %q", raw)
}
