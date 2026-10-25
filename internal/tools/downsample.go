package tools

// Series is a summarised numeric series with a bounded number of points.
type Series struct {
	Min    float64      `json:"min"`
	Max    float64      `json:"max"`
	Mean   float64      `json:"mean"`
	Count  int          `json:"count"`
	Points [][2]float64 `json:"points"`
}

// Downsample computes statistics over all input points and returns an evenly
// spaced subset of at most max points, always including the first and last.
// Inputs with len <= max keep all points.
func Downsample(points [][2]float64, max int) Series {
	s := Series{Count: len(points), Points: points}
	if len(points) == 0 {
		s.Points = [][2]float64{}
		return s
	}
	s.Min, s.Max = points[0][1], points[0][1]
	var sum float64
	for _, p := range points {
		v := p[1]
		if v < s.Min {
			s.Min = v
		}
		if v > s.Max {
			s.Max = v
		}
		sum += v
	}
	s.Mean = sum / float64(len(points))

	if len(points) <= max {
		return s
	}
	if max < 2 {
		max = 2
	}
	out := make([][2]float64, 0, max)
	last := len(points) - 1
	for i := 0; i < max; i++ {
		out = append(out, points[i*last/(max-1)])
	}
	s.Points = out
	return s
}
