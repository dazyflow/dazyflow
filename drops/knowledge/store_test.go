// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package knowledge

import (
	"math"
	"testing"
)

func TestVectorRoundTrip(t *testing.T) {
	in := []float32{0, 1, -1, 0.25, 1e-7, 12345.678}
	out := decodeVector(encodeVector(in))
	if len(out) != len(in) {
		t.Fatalf("got %d numbers, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("[%d] = %v, want %v", i, out[i], in[i])
		}
	}
}

// Normalising on the way in is what lets a search be a dot product.
func TestNormalize(t *testing.T) {
	v := normalize([]float32{3, 4})
	if math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Errorf("got %v, want the unit vector", v)
	}
	if got := dot(v, v); math.Abs(got-1) > 1e-6 {
		t.Errorf("a unit vector against itself = %v, want 1", got)
	}
	// A zero vector has no direction to give it; it must not become NaN.
	zero := normalize([]float32{0, 0})
	if math.IsNaN(float64(zero[0])) || math.IsNaN(float64(zero[1])) {
		t.Errorf("zero vector normalised to %v", zero)
	}
}

func TestDotIsTheCosine(t *testing.T) {
	same := dot(normalize([]float32{1, 1}), normalize([]float32{2, 2}))
	if math.Abs(same-1) > 1e-6 {
		t.Errorf("parallel vectors scored %v, want 1", same)
	}
	perpendicular := dot(normalize([]float32{1, 0}), normalize([]float32{0, 1}))
	if math.Abs(perpendicular) > 1e-6 {
		t.Errorf("perpendicular vectors scored %v, want 0", perpendicular)
	}
}
