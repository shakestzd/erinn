package main

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/models"
)

// archivedNode pairs a reconstructed node with its graph node type so edge
// indexing can label the from-node correctly.
type archivedNode struct {
	node     *models.Node
	nodeType string
}

// loadArchiveLedgerNodes reads every archive ledger under .wipnote/archive/ and
// returns the reconstructed nodes paired with their graph node type. The set of
// ledgers is graph.ArchiveLedgerCollections (the single source of truth shared
// with LoadAll and the archive command). Each node is parsed from the lossless
// preserved HTML, so it is identical to what an individual file would have
// yielded. A missing ledger is not an error (it contributes no nodes).
func loadArchiveLedgerNodes(wipnoteDir string, verbose bool) ([]archivedNode, int) {
	var out []archivedNode
	errCount := 0
	for _, col := range graph.ArchiveLedgerCollections {
		path := graph.ArchiveLedgerPath(wipnoteDir, col)
		entries, err := graph.ReadLedger(path)
		if err != nil {
			if !os.IsNotExist(err) {
				errCount++
				if verbose {
					fmt.Printf("reindex archive: read ledger error: %s: %v\n", path, err)
				}
			}
			continue
		}
		nodeType := collectionNodeType(col)
		for _, e := range entries {
			node, nodeErr := e.Node()
			if nodeErr != nil {
				errCount++
				if verbose {
					fmt.Printf("reindex archive: parse row error: %s: %v\n", e.ID, nodeErr)
				}
				continue
			}
			out = append(out, archivedNode{node: node, nodeType: nodeType})
		}
	}
	return out, errCount
}

// collectionNodeType maps an archive ledger collection directory name to the
// graph node type used for edge rows.
func collectionNodeType(collectionDir string) string {
	switch collectionDir {
	case "features":
		return "feature"
	case "bugs":
		return "bug"
	case "spikes":
		return "spike"
	default:
		return "feature"
	}
}

// reindexWorkitemLedgerNodes upserts every archived work item into the features
// read index, routing through the SAME indexWorkitemNode path used for live
// files. Returns (total, upserted, errCount). gitPath is "" for archived items
// because they have no standalone file; their canonical timestamps come from the
// preserved HTML. Validity is recorded in validIDs so edges and stale-purge see
// archived rows as live — they are canonical, just compacted.
func reindexWorkitemLedgerNodes(database *sql.DB, wipnoteDir, projectDir string, validIDs map[string]bool, verbose bool) (int, int, int) {
	nodes, loadErrs := loadArchiveLedgerNodes(wipnoteDir, verbose)
	total, upserted, errCount := 0, 0, loadErrs
	for _, an := range nodes {
		total++
		if indexWorkitemNode(database, an.node, projectDir, "", nil, validIDs, verbose) {
			upserted++
		} else {
			errCount++
		}
	}
	return total, upserted, errCount
}

// reindexWorkitemLedgerEdges inserts graph edges for every archived work item
// whose targets are valid, mirroring reindexEdges for the file-backed path so
// lineage/trace traverse to and from archived rows.
func reindexWorkitemLedgerEdges(database *sql.DB, wipnoteDir string, validIDs map[string]bool, verbose bool) {
	nodes, _ := loadArchiveLedgerNodes(wipnoteDir, verbose)
	for _, an := range nodes {
		if !validIDs[an.node.ID] {
			continue
		}
		indexNodeEdges(database, an.node, an.nodeType, validIDs)
	}
}
