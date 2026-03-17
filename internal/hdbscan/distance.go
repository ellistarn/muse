package hdbscan

import "math"

// CosineDistanceMatrix computes pairwise cosine distances between vectors.
// Cosine distance = 1 - cosine_similarity.
func CosineDistanceMatrix(vectors [][]float64) [][]float64 {
	n := len(vectors)
	dist := make([][]float64, n)
	for i := 0; i < n; i++ {
		dist[i] = make([]float64, n)
	}
	// Precompute norms
	norms := make([]float64, n)
	for i, v := range vectors {
		var sum float64
		for _, x := range v {
			sum += x * x
		}
		norms[i] = math.Sqrt(sum)
	}

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			d := cosineDistance(vectors[i], vectors[j], norms[i], norms[j])
			dist[i][j] = d
			dist[j][i] = d
		}
	}
	return dist
}

func cosineDistance(a, b []float64, normA, normB float64) float64 {
	if normA == 0 || normB == 0 {
		return 1.0
	}
	var dot float64
	for i := range a {
		dot += a[i] * b[i]
	}
	sim := dot / (normA * normB)
	// Clamp to [-1, 1] to handle floating point errors
	if sim > 1 {
		sim = 1
	}
	if sim < -1 {
		sim = -1
	}
	return 1 - sim
}
