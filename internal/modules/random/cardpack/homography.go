package cardpack

import "math"

// homography is a 3x3 projective map, row-major: a point (x, y) maps to
// ((m0x + m1y + m2) / w, (m3x + m4y + m5) / w) with w = m6x + m7y + m8.
type homography [9]float64

// squareToQuad maps the unit square's corners (0,0), (1,0), (1,1), (0,1) to
// q's corners in order (Heckbert's closed form).
func squareToQuad(q quad) homography {
	x0, y0, x1, y1, x2, y2, x3, y3 := q[0], q[1], q[2], q[3], q[4], q[5], q[6], q[7]
	dx1, dx2, dx3 := x1-x2, x3-x2, x0-x1+x2-x3
	dy1, dy2, dy3 := y1-y2, y3-y2, y0-y1+y2-y3
	var g, h float64
	if den := dx1*dy2 - dx2*dy1; den != 0 {
		g = (dx3*dy2 - dx2*dy3) / den
		h = (dx1*dy3 - dx3*dy1) / den
	}
	return homography{
		x1 - x0 + g*x1, x3 - x0 + h*x3, x0,
		y1 - y0 + g*y1, y3 - y0 + h*y3, y0,
		g, h, 1,
	}
}

func (m homography) apply(x, y float64) (float64, float64) {
	w := m[6]*x + m[7]*y + m[8]
	return (m[0]*x + m[1]*y + m[2]) / w, (m[3]*x + m[4]*y + m[5]) / w
}

// inverse returns the inverse map, or false when the quad has collapsed (the
// card seen edge on).
func (m homography) inverse() (homography, bool) {
	a := homography{
		m[4]*m[8] - m[5]*m[7], m[2]*m[7] - m[1]*m[8], m[1]*m[5] - m[2]*m[4],
		m[5]*m[6] - m[3]*m[8], m[0]*m[8] - m[2]*m[6], m[2]*m[3] - m[0]*m[5],
		m[3]*m[7] - m[4]*m[6], m[1]*m[6] - m[0]*m[7], m[0]*m[4] - m[1]*m[3],
	}
	det := m[0]*a[0] + m[1]*a[3] + m[2]*a[6]
	if math.Abs(det) < 1e-9 {
		return homography{}, false
	}
	for i := range a {
		a[i] /= det
	}
	return a, true
}
