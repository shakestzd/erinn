package main

import (
	"fmt"

	"github.com/google/uuid"
	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/models"
	"github.com/spf13/cobra"
)

func backfillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Backfill derived tables from existing data",
	}
	cmd.AddCommand(backfillFeatureFilesCmd())
	cmd.AddCommand(backfillToolCallsFeatureCmd())
	return cmd
}

func backfillFeatureFilesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "feature-files",
		Short: "Populate feature_files from existing tool_calls + sessions",
		Long: `Retroactively populates the feature_files table by joining tool_calls
with sessions.active_feature_id. Safe to run multiple times — uses ON CONFLICT upsert.`,
		RunE: runBackfillFeatureFiles,
	}
}

func backfillToolCallsFeatureCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tool-calls-feature",
		Short: "Populate tool_calls.feature_id from sessions.active_feature_id",
		Long: `Retroactively sets feature_id on existing tool_calls rows where it is
empty, using the session's active_feature_id. Safe to run multiple times.`,
		RunE: runBackfillToolCallsFeature,
	}
}

func runBackfillFeatureFiles(_ *cobra.Command, _ []string) error {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	database, err := openDB(htmlgraphDir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	rows, err := database.Query(`
		SELECT tc.session_id, s.active_feature_id, tc.tool_name, tc.input_json
		FROM tool_calls tc
		JOIN sessions s ON tc.session_id = s.session_id
		WHERE s.active_feature_id IS NOT NULL
		  AND s.active_feature_id != ''
		  AND tc.tool_name IN ('Read', 'Edit', 'MultiEdit', 'Write', 'Glob', 'Grep')
		  AND tc.input_json IS NOT NULL
		  AND tc.input_json != ''
	`)
	if err != nil {
		return fmt.Errorf("query tool_calls: %w", err)
	}
	defer rows.Close()

	var total, upserted int
	for rows.Next() {
		var sessionID, featureID, toolName, inputJSON string
		if err := rows.Scan(&sessionID, &featureID, &toolName, &inputJSON); err != nil {
			continue
		}
		total++

		op := ingestFileOp(toolName)
		if op == "" {
			continue
		}
		fp := extractIngestFilePath(inputJSON)
		if fp == "" {
			continue
		}

		ff := &models.FeatureFile{
			ID:        featureID + "-" + uuid.NewString(),
			FeatureID: featureID,
			FilePath:  fp,
			Operation: op,
			SessionID: sessionID,
		}
		if err := dbpkg.UpsertFeatureFile(database, ff); err == nil {
			upserted++
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating rows: %w", err)
	}

	fmt.Printf("Scanned %d tool calls, upserted %d feature-file links\n", total, upserted)
	return nil
}

func runBackfillToolCallsFeature(_ *cobra.Command, _ []string) error {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	database, err := openDB(htmlgraphDir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	result, err := database.Exec(`
		UPDATE tool_calls
		SET feature_id = (
			SELECT s.active_feature_id
			FROM sessions s
			WHERE s.session_id = tool_calls.session_id
			  AND s.active_feature_id IS NOT NULL
			  AND s.active_feature_id != ''
		)
		WHERE (feature_id IS NULL OR feature_id = '')
		  AND EXISTS (
			SELECT 1 FROM sessions s
			WHERE s.session_id = tool_calls.session_id
			  AND s.active_feature_id IS NOT NULL
			  AND s.active_feature_id != ''
		  )
	`)
	if err != nil {
		return fmt.Errorf("update tool_calls.feature_id: %w", err)
	}

	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}

	fmt.Printf("Updated %d tool_calls rows with feature_id\n", updated)
	return nil
}
