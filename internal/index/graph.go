package index

import (
	"sort"
)

// GraphNode is one visualisable note.
type GraphNode struct {
	Path  string `json:"path"`
	Title string `json:"title"`
}

// GraphEdge is one directed wikilink src → dst.
type GraphEdge struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

// Graph returns every indexed note as a node plus every stored backlink
// edge, both in lexical order. The vault filesystem is the source of truth;
// this index is derived and rebuildable (tech-stack §3.3), so Graph never
// synthesizes nodes for link targets with no note row — edges may dangle
// until the target note is created.
func (ix *Index) Graph() ([]GraphNode, []GraphEdge, error) {
	rows, err := ix.DB.Query(`SELECT path, title, tags FROM notes ORDER BY path`)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	var nodes []GraphNode
	for rows.Next() {
		var path, title, tags string
		if err := rows.Scan(&path, &title, &tags); err != nil {
			return nil, nil, err
		}
		nodes = append(nodes, GraphNode{Path: path, Title: title})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	erows, err := ix.DB.Query(`SELECT src, dst FROM backlinks ORDER BY src, dst`)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = erows.Close() }()
	var edges []GraphEdge
	for erows.Next() {
		var e GraphEdge
		if err := erows.Scan(&e.Src, &e.Dst); err != nil {
			return nil, nil, err
		}
		edges = append(edges, e)
	}
	if err := erows.Err(); err != nil {
		return nil, nil, err
	}
	if nodes == nil {
		nodes = []GraphNode{}
	}
	if edges == nil {
		edges = []GraphEdge{}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Path < nodes[j].Path })
	return nodes, edges, nil
}
