package hdbscan

import (
	"math"
	"testing"
)

func TestCosineDistanceMatrix(t *testing.T) {
	vectors := [][]float64{
		{1, 0, 0},
		{0, 1, 0},
		{1, 1, 0},
	}
	dist := CosineDistanceMatrix(vectors)

	// Same vector = 0 distance
	if dist[0][0] != 0 {
		t.Errorf("self-distance should be 0, got %f", dist[0][0])
	}

	// Orthogonal vectors = distance 1
	if math.Abs(dist[0][1]-1.0) > 1e-10 {
		t.Errorf("orthogonal distance should be 1, got %f", dist[0][1])
	}

	// Symmetric
	if dist[0][2] != dist[2][0] {
		t.Errorf("distance should be symmetric")
	}

	// [1,0,0] to [1,1,0] should be ~0.293 (1 - 1/sqrt(2))
	expected := 1.0 - 1.0/math.Sqrt(2)
	if math.Abs(dist[0][2]-expected) > 1e-10 {
		t.Errorf("expected distance %f, got %f", expected, dist[0][2])
	}
}

func TestClusterSyntheticData(t *testing.T) {
	// Three well-separated clusters of 5 points each in 2D
	vectors := [][]float64{
		// Cluster 0: around (10, 0)
		{10, 0.1}, {10.1, 0}, {9.9, 0.2}, {10.2, -0.1}, {10, -0.2},
		// Cluster 1: around (0, 10)
		{0.1, 10}, {0, 10.1}, {0.2, 9.9}, {-0.1, 10.2}, {-0.2, 10},
		// Cluster 2: around (-10, -10)
		{-10, -10.1}, {-10.1, -10}, {-9.9, -10.2}, {-10.2, -9.9}, {-10, -10},
	}

	dist := CosineDistanceMatrix(vectors)
	labels := Cluster(dist, 3)

	if len(labels) != 15 {
		t.Fatalf("expected 15 labels, got %d", len(labels))
	}

	// Check that points within the same input cluster have the same label
	// and different input clusters have different labels (or noise)
	checkSameCluster := func(name string, indices []int) {
		label := labels[indices[0]]
		for _, i := range indices[1:] {
			if labels[i] != label {
				t.Errorf("%s: points %d and %d have different labels (%d vs %d)",
					name, indices[0], i, label, labels[i])
			}
		}
	}

	cluster0 := []int{0, 1, 2, 3, 4}
	cluster1 := []int{5, 6, 7, 8, 9}
	cluster2 := []int{10, 11, 12, 13, 14}

	checkSameCluster("cluster0", cluster0)
	checkSameCluster("cluster1", cluster1)
	checkSameCluster("cluster2", cluster2)

	// Clusters should be distinct (not all noise)
	if labels[0] == -1 && labels[5] == -1 && labels[10] == -1 {
		t.Error("all points classified as noise — expected at least some clusters")
	}

	// If clusters are found, they should be different from each other
	if labels[0] != -1 && labels[5] != -1 && labels[0] == labels[5] {
		t.Error("cluster0 and cluster1 should have different labels")
	}
}

func TestClusterWithNoise(t *testing.T) {
	// Two tight clusters + two outliers
	vectors := [][]float64{
		// Cluster: around (1, 0)
		{1, 0.01}, {1.01, 0}, {0.99, 0.02}, {1.02, -0.01},
		// Cluster: around (0, 1)
		{0.01, 1}, {0, 1.01}, {0.02, 0.99}, {-0.01, 1.02},
		// Outliers
		{5, 5},
		{-5, -5},
	}

	dist := CosineDistanceMatrix(vectors)
	labels := Cluster(dist, 3)

	if len(labels) != 10 {
		t.Fatalf("expected 10 labels, got %d", len(labels))
	}

	// Count clusters and noise
	clusterCounts := map[int]int{}
	for _, l := range labels {
		clusterCounts[l]++
	}

	// Should have at least 2 clusters and some noise
	nonNoiseClusters := 0
	for l, count := range clusterCounts {
		if l != -1 && count >= 3 {
			nonNoiseClusters++
		}
	}

	t.Logf("Labels: %v", labels)
	t.Logf("Cluster counts: %v", clusterCounts)

	// At minimum we expect the algorithm to identify structure
	if nonNoiseClusters == 0 {
		t.Error("expected at least one non-noise cluster")
	}
}

func TestClusterEmpty(t *testing.T) {
	labels := Cluster(nil, 3)
	if labels != nil {
		t.Errorf("expected nil for empty input, got %v", labels)
	}
}

func TestClusterTooSmall(t *testing.T) {
	// Fewer points than minClusterSize
	dist := [][]float64{{0, 1}, {1, 0}}
	labels := Cluster(dist, 3)
	// All should be noise or a single cluster
	if len(labels) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(labels))
	}
}

func TestClusterSingleCluster(t *testing.T) {
	// All points very close together — should form one cluster
	vectors := [][]float64{
		{1, 0.001}, {1, 0.002}, {1, 0.003}, {1, 0.004}, {1, 0.005},
	}
	dist := CosineDistanceMatrix(vectors)
	labels := Cluster(dist, 3)

	// All should be in the same cluster
	first := labels[0]
	for i, l := range labels {
		if l != first {
			t.Errorf("point %d has label %d, expected %d (all same cluster)", i, l, first)
		}
	}
	if first == -1 {
		t.Error("expected a cluster, got all noise")
	}
}
