package migration

import (
	"encoding/csv"
	"encoding/xml"
	"strings"
)

func mremoteCSV(text string) (Result, error) {
	reader := csv.NewReader(strings.NewReader(text))
	first, _, _ := strings.Cut(text, "\n")
	if strings.Contains(first, ";") {
		reader.Comma = ';'
	}
	rows, err := reader.ReadAll()
	if err != nil || len(rows) < 2 {
		return Result{}, ErrFormat
	}
	headers := iniSection{}
	for _, k := range rows[0] {
		headers.pairs = append(headers.pairs, iniPair{key: k, value: "1"})
	}
	if headers.get("Hostname") == "" || headers.get("Protocol") == "" || headers.get("Name") == "" {
		return Result{}, ErrFormat
	}
	nodes := make([]mremoteNode, 0, len(rows)-1)
	ids := map[string]int{}
	parents := []string{}
	for _, row := range rows[1:] {
		node := mremoteNode{}
		s := iniSection{}
		for i, v := range row {
			key := rows[0][i]
			s.pairs = append(s.pairs, iniPair{key: key, value: v})
			if key == "NodeType" {
				key = "Type"
			}
			if key == "Description" {
				key = "Descr"
			}
			node.Attrs = append(node.Attrs, xml.Attr{Name: xml.Name{Local: key}, Value: v})
		}
		if id := s.get("Id"); id != "" {
			if _, ok := ids[id]; ok {
				return Result{}, ErrFormat
			}
			ids[id] = len(nodes)
		}
		nodes = append(nodes, node)
		parents = append(parents, s.get("Parent"))
	}
	children := map[int][]int{}
	roots := []int{}
	for i, parent := range parents {
		if p, ok := ids[parent]; ok {
			children[p] = append(children[p], i)
		} else {
			roots = append(roots, i)
		}
	}
	count := 0
	var build func(int, int) (mremoteNode, error)
	build = func(i, depth int) (mremoteNode, error) {
		if depth > 64 {
			return mremoteNode{}, ErrFormat
		}
		count++
		n := nodes[i]
		for _, child := range children[i] {
			c, err := build(child, depth+1)
			if err != nil {
				return mremoteNode{}, err
			}
			n.Nodes = append(n.Nodes, c)
		}
		return n, nil
	}
	tree := []mremoteNode{}
	for _, root := range roots {
		n, err := build(root, 0)
		if err != nil {
			return Result{}, err
		}
		tree = append(tree, n)
	}
	if count != len(nodes) {
		return Result{}, ErrFormat
	}
	r := Result{}
	err = mremoteWalk(tree, iniSection{}, "", 0, &r)
	return r, err
}
