// Package hdbscan implements the HDBSCAN clustering algorithm.
//
// HDBSCAN discovers clusters of varying density and explicitly labels noise.
// This implementation targets small datasets (hundreds to low thousands of
// points) and prioritizes correctness over asymptotic performance.
package hdbscan

import (
	"math"
	"sort"
)

// Cluster runs HDBSCAN on the given distance matrix with the specified
// minimum cluster size. Returns a label for each point: non-negative values
// are cluster IDs, -1 indicates noise.
func Cluster(distMatrix [][]float64, minClusterSize int) []int {
	n := len(distMatrix)
	if n == 0 {
		return nil
	}
	if minClusterSize < 2 {
		minClusterSize = 2
	}

	coreDists := coreDistances(distMatrix, minClusterSize)
	mrDist := mutualReachability(distMatrix, coreDists)
	mst := primMST(mrDist)
	sort.Slice(mst, func(i, j int) bool { return mst[i].weight < mst[j].weight })

	dendro := buildDendrogram(n, mst)
	return extractFromDendrogram(n, dendro, minClusterSize)
}

type edge struct {
	u, v   int
	weight float64
}

// dendroNode represents a node in the single-linkage dendrogram.
// Leaf nodes (indices 0..n-1) represent individual points.
// Internal nodes (indices n..2n-2) represent merges.
type dendroNode struct {
	left, right int     // child node indices (-1 for leaves)
	weight      float64 // merge distance
	size        int     // number of points in subtree
}

// buildDendrogram constructs a single-linkage dendrogram from sorted MST edges.
// Returns a slice of dendrogram nodes. Indices 0..n-1 are leaves (one per point),
// n..2n-2 are internal merge nodes.
func buildDendrogram(n int, sortedEdges []edge) []dendroNode {
	nodes := make([]dendroNode, n, 2*n-1)
	for i := 0; i < n; i++ {
		nodes[i] = dendroNode{left: -1, right: -1, size: 1}
	}

	uf := newUnionFind(n)
	// Map from union-find root to dendrogram node index
	rootToNode := make([]int, n)
	for i := 0; i < n; i++ {
		rootToNode[i] = i
	}

	for _, e := range sortedEdges {
		ru := uf.find(e.u)
		rv := uf.find(e.v)
		if ru == rv {
			continue
		}

		nodeU := rootToNode[ru]
		nodeV := rootToNode[rv]

		// Create internal node
		newNode := dendroNode{
			left:   nodeU,
			right:  nodeV,
			weight: e.weight,
			size:   nodes[nodeU].size + nodes[nodeV].size,
		}
		newIdx := len(nodes)
		nodes = append(nodes, newNode)

		uf.union(ru, rv)
		rootToNode[uf.find(ru)] = newIdx
	}

	return nodes
}

// condensedCluster represents a cluster in the condensed tree.
type condensedCluster struct {
	id        int
	parent    int
	children  []int
	birthLam  float64 // lambda when this cluster was born (1/distance)
	points    []int   // leaf point indices that belong to this cluster
	stability float64
	fallenOut []fallenPoint // points that fell out with their lambda
}

type fallenPoint struct {
	point  int
	lambda float64
}

// extractFromDendrogram walks the dendrogram top-down to build a condensed
// cluster tree, then selects stable clusters via excess-of-mass.
func extractFromDendrogram(n int, dendro []dendroNode, minClusterSize int) []int {
	if len(dendro) == 0 || n == 0 {
		labels := make([]int, n)
		for i := range labels {
			labels[i] = -1
		}
		return labels
	}

	root := len(dendro) - 1 // last node is the root

	// If total points < minClusterSize, everything is noise.
	if dendro[root].size < minClusterSize {
		labels := make([]int, n)
		for i := range labels {
			labels[i] = -1
		}
		return labels
	}

	// Build condensed tree by walking dendrogram top-down.
	var clusters []condensedCluster
	nextID := 0

	// Recursive function to process a dendrogram node within a cluster context.
	// clusterIdx is the current condensed cluster that this subtree belongs to.
	// Returns the set of points in this subtree.
	var processNode func(nodeIdx, clusterIdx int) []int
	processNode = func(nodeIdx, clusterIdx int) []int {
		node := &dendro[nodeIdx]

		// Leaf node: return single point
		if node.left == -1 {
			return []int{nodeIdx}
		}

		leftSize := dendro[node.left].size
		rightSize := dendro[node.right].size

		lambda := 0.0
		if node.weight > 0 {
			lambda = 1.0 / node.weight
		}

		if leftSize >= minClusterSize && rightSize >= minClusterSize {
			// Both children are large enough: cluster splits into two children.
			// Current cluster "dies" at this lambda.
			childA := nextID
			nextID++
			childB := nextID
			nextID++
			clusters = append(clusters,
				condensedCluster{id: childA, parent: clusterIdx, birthLam: lambda},
				condensedCluster{id: childB, parent: clusterIdx, birthLam: lambda},
			)
			if clusterIdx >= 0 {
				clusters[clusterIdx].children = append(clusters[clusterIdx].children, childA, childB)
			}

			pointsA := processNode(node.left, childA)
			pointsB := processNode(node.right, childB)

			return append(pointsA, pointsB...)

		} else if leftSize >= minClusterSize {
			// Right side is too small: those points fall out as noise.
			rightPoints := collectLeaves(dendro, node.right)
			if clusterIdx >= 0 {
				for _, p := range rightPoints {
					clusters[clusterIdx].fallenOut = append(clusters[clusterIdx].fallenOut, fallenPoint{p, lambda})
				}
			}
			leftPoints := processNode(node.left, clusterIdx)
			return append(leftPoints, rightPoints...)

		} else if rightSize >= minClusterSize {
			// Left side is too small.
			leftPoints := collectLeaves(dendro, node.left)
			if clusterIdx >= 0 {
				for _, p := range leftPoints {
					clusters[clusterIdx].fallenOut = append(clusters[clusterIdx].fallenOut, fallenPoint{p, lambda})
				}
			}
			rightPoints := processNode(node.right, clusterIdx)
			return append(leftPoints, rightPoints...)

		} else {
			// Both sides too small: they merge within the current cluster.
			leftPoints := collectLeaves(dendro, node.left)
			rightPoints := collectLeaves(dendro, node.right)
			return append(leftPoints, rightPoints...)
		}
	}

	// Start: the root itself is the first cluster.
	rootCluster := nextID
	nextID++
	clusters = append(clusters, condensedCluster{
		id:       rootCluster,
		parent:   -1,
		birthLam: 0,
	})

	allPoints := processNode(root, rootCluster)
	_ = allPoints

	// If only one cluster (no splits), all points are one cluster.
	if len(clusters) == 1 {
		labels := make([]int, n)
		return labels
	}

	// Compute stability for each cluster.
	// Stability = sum over all points that fell out of (lambda_out - lambda_birth)
	for i := range clusters {
		cl := &clusters[i]
		for _, fp := range cl.fallenOut {
			cl.stability += fp.lambda - cl.birthLam
		}
	}

	// Select clusters using excess-of-mass.
	selected := selectClusters(clusters)

	// Assign points to selected clusters.
	return assignPoints(n, clusters, selected, dendro, minClusterSize)
}

// collectLeaves returns all leaf point indices under a dendrogram node.
func collectLeaves(dendro []dendroNode, nodeIdx int) []int {
	node := &dendro[nodeIdx]
	if node.left == -1 {
		return []int{nodeIdx}
	}
	left := collectLeaves(dendro, node.left)
	right := collectLeaves(dendro, node.right)
	return append(left, right...)
}

// selectClusters walks the condensed tree bottom-up, selecting either a
// parent node or its children based on excess-of-mass stability.
func selectClusters(clusters []condensedCluster) map[int]bool {
	selected := make(map[int]bool)

	// Mark leaves as selected initially.
	for i := range clusters {
		if len(clusters[i].children) == 0 {
			selected[clusters[i].id] = true
		}
	}

	// Walk bottom-up through the tree. Process children before parents.
	// Since children always have higher indices than parents (by construction),
	// process in reverse order.
	for i := len(clusters) - 1; i >= 0; i-- {
		cl := &clusters[i]
		if len(cl.children) == 0 {
			continue
		}
		childStability := 0.0
		for _, cid := range cl.children {
			childStability += clusters[cid].stability
		}
		if cl.stability >= childStability {
			// Parent is more stable: select it, deselect children.
			for _, cid := range cl.children {
				deselect(clusters, cid, selected)
			}
			selected[cl.id] = true
		} else {
			// Children are more stable: propagate stability up.
			cl.stability = childStability
		}
	}

	return selected
}

func deselect(clusters []condensedCluster, id int, selected map[int]bool) {
	delete(selected, id)
	for _, cid := range clusters[id].children {
		deselect(clusters, cid, selected)
	}
}

// assignPoints determines which selected cluster each point belongs to.
// Walks the dendrogram again, tracking cluster assignments.
func assignPoints(n int, clusters []condensedCluster, selected map[int]bool, dendro []dendroNode, minClusterSize int) []int {
	labels := make([]int, n)
	for i := range labels {
		labels[i] = -1
	}

	// Build label map: selected cluster ID → output label (0, 1, 2, ...)
	labelMap := make(map[int]int)
	nextLabel := 0
	for id := range selected {
		labelMap[id] = nextLabel
		nextLabel++
	}

	// Walk the condensed tree to assign each point to its deepest selected cluster.
	var assignFromCluster func(clusterID int, parentLabel int)
	assignFromCluster = func(clusterID int, parentLabel int) {
		cl := &clusters[clusterID]
		myLabel := parentLabel
		if label, ok := labelMap[clusterID]; ok {
			myLabel = label
		}

		// Points that fell out at this cluster get its label.
		for _, fp := range cl.fallenOut {
			labels[fp.point] = myLabel
		}

		// Recurse into children.
		for _, childID := range cl.children {
			assignFromCluster(childID, myLabel)
		}
	}

	// Start from root cluster (index 0).
	assignFromCluster(0, -1)

	// Points not assigned by fallenOut are leaves that never fell out —
	// they belong to the deepest cluster in their path.
	// Walk the dendrogram to find these.
	assignLeafPoints(dendro, clusters, selected, labelMap, labels, len(dendro)-1, 0, minClusterSize)

	return labels
}

// assignLeafPoints walks the dendrogram and assigns leaf points that weren't
// captured by the fallenOut mechanism (i.e., points that stayed in a cluster
// all the way to the bottom of the tree).
func assignLeafPoints(dendro []dendroNode, clusters []condensedCluster, selected map[int]bool, labelMap map[int]int, labels []int, nodeIdx, currentCluster, minClusterSize int) {
	node := &dendro[nodeIdx]

	if node.left == -1 {
		// Leaf point
		if labels[nodeIdx] == -1 {
			if label, ok := labelMap[currentCluster]; ok {
				labels[nodeIdx] = label
			}
		}
		return
	}

	leftSize := dendro[node.left].size
	rightSize := dendro[node.right].size

	if leftSize >= minClusterSize && rightSize >= minClusterSize {
		// Split: find the two child cluster IDs.
		cl := &clusters[currentCluster]
		if len(cl.children) >= 2 {
			assignLeafPoints(dendro, clusters, selected, labelMap, labels, node.left, cl.children[0], minClusterSize)
			assignLeafPoints(dendro, clusters, selected, labelMap, labels, node.right, cl.children[1], minClusterSize)
		}
	} else if leftSize >= minClusterSize {
		// Left continues, right fell out
		assignLeafPoints(dendro, clusters, selected, labelMap, labels, node.left, currentCluster, minClusterSize)
		assignLeafPoints(dendro, clusters, selected, labelMap, labels, node.right, currentCluster, minClusterSize)
	} else if rightSize >= minClusterSize {
		assignLeafPoints(dendro, clusters, selected, labelMap, labels, node.left, currentCluster, minClusterSize)
		assignLeafPoints(dendro, clusters, selected, labelMap, labels, node.right, currentCluster, minClusterSize)
	} else {
		assignLeafPoints(dendro, clusters, selected, labelMap, labels, node.left, currentCluster, minClusterSize)
		assignLeafPoints(dendro, clusters, selected, labelMap, labels, node.right, currentCluster, minClusterSize)
	}
}

type unionFind struct {
	parent []int
	rank   []int
}

func newUnionFind(n int) *unionFind {
	parent := make([]int, n)
	rank := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	return &unionFind{parent: parent, rank: rank}
}

func (uf *unionFind) find(x int) int {
	if uf.parent[x] != x {
		uf.parent[x] = uf.find(uf.parent[x])
	}
	return uf.parent[x]
}

func (uf *unionFind) union(x, y int) {
	rx, ry := uf.find(x), uf.find(y)
	if rx == ry {
		return
	}
	if uf.rank[rx] < uf.rank[ry] {
		rx, ry = ry, rx
	}
	uf.parent[ry] = rx
	if uf.rank[rx] == uf.rank[ry] {
		uf.rank[rx]++
	}
}

func coreDistances(distMatrix [][]float64, minPts int) []float64 {
	n := len(distMatrix)
	coreDists := make([]float64, n)
	for i := 0; i < n; i++ {
		dists := make([]float64, 0, n-1)
		for j := 0; j < n; j++ {
			if i != j {
				dists = append(dists, distMatrix[i][j])
			}
		}
		sort.Float64s(dists)
		idx := minPts - 2
		if idx < 0 {
			idx = 0
		}
		if idx >= len(dists) {
			idx = len(dists) - 1
		}
		coreDists[i] = dists[idx]
	}
	return coreDists
}

func mutualReachability(distMatrix [][]float64, coreDists []float64) [][]float64 {
	n := len(distMatrix)
	mrd := make([][]float64, n)
	for i := 0; i < n; i++ {
		mrd[i] = make([]float64, n)
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			d := distMatrix[i][j]
			if coreDists[i] > d {
				d = coreDists[i]
			}
			if coreDists[j] > d {
				d = coreDists[j]
			}
			mrd[i][j] = d
		}
	}
	return mrd
}

func primMST(dist [][]float64) []edge {
	n := len(dist)
	if n <= 1 {
		return nil
	}
	inMST := make([]bool, n)
	minEdge := make([]float64, n)
	parent := make([]int, n)
	for i := range minEdge {
		minEdge[i] = math.Inf(1)
		parent[i] = -1
	}
	inMST[0] = true
	for j := 1; j < n; j++ {
		minEdge[j] = dist[0][j]
		parent[j] = 0
	}
	var edges []edge
	for range n - 1 {
		u := -1
		minW := math.Inf(1)
		for j := 0; j < n; j++ {
			if !inMST[j] && minEdge[j] < minW {
				minW = minEdge[j]
				u = j
			}
		}
		if u == -1 {
			break
		}
		inMST[u] = true
		edges = append(edges, edge{u: parent[u], v: u, weight: minW})
		for j := 0; j < n; j++ {
			if !inMST[j] && dist[u][j] < minEdge[j] {
				minEdge[j] = dist[u][j]
				parent[j] = u
			}
		}
	}
	return edges
}
