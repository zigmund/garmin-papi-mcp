package tools

import (
	"math"
	"testing"
)

func seq(n int) [][2]float64 {
	p := make([][2]float64, n)
	for i := range p {
		p[i] = [2]float64{float64(i), float64(i)}
	}
	return p
}

func TestDownsampleKeepsStats(t *testing.T) {
	in := seq(1000) // values 0..999
	s := Downsample(in, 10)
	if s.Count != 1000 || s.Min != 0 || s.Max != 999 || math.Abs(s.Mean-499.5) > 1e-9 {
		t.Fatalf("stats not from full input: %+v", s)
	}
	if len(s.Points) > 10 {
		t.Fatalf("too many points: %d", len(s.Points))
	}
}

func TestDownsampleIncludesEndpoints(t *testing.T) {
	for _, n := range []int{11, 100, 1001} {
		for _, max := range []int{2, 3, 7, 10} {
			in := seq(n)
			s := Downsample(in, max)
			if len(s.Points) > max || len(s.Points) < 2 {
				t.Fatalf("n=%d max=%d: len %d", n, max, len(s.Points))
			}
			if s.Points[0] != in[0] || s.Points[len(s.Points)-1] != in[n-1] {
				t.Fatalf("n=%d max=%d: endpoints missing: %v", n, max, s.Points)
			}
			for i := 1; i < len(s.Points); i++ {
				if s.Points[i][0] <= s.Points[i-1][0] {
					t.Fatalf("points not strictly increasing: %v", s.Points)
				}
			}
		}
	}
}

func TestDownsampleSmallInputUnchanged(t *testing.T) {
	in := seq(5)
	s := Downsample(in, 5)
	if len(s.Points) != 5 || s.Count != 5 {
		t.Fatalf("%+v", s)
	}
	for i := range in {
		if s.Points[i] != in[i] {
			t.Fatal("changed")
		}
	}
	e := Downsample(nil, 5)
	if e.Count != 0 || len(e.Points) != 0 {
		t.Fatalf("%+v", e)
	}
}
