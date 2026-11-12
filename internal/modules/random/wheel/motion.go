package wheel

import (
	"math"
	"math/rand/v2"
)

// The spin is simulated, not eased: a hand pulls the wheel back and pushes
// it up to speed, then bearing friction and air drag slow it while every
// peg it carries strikes the pointer, a rubber flapper on a torsional
// spring. Each strike costs the wheel the energy it takes to bend the
// flapper aside, so it ticks down peg by peg at the end, and when it is too
// slow to push the next peg past, the flapper springs back and rocks the
// wheel backwards into the previous segment. Angles are radians, clockwise
// from 12 o'clock; lengths are output pixels.
const (
	simStep    = 1.0 / 12000 // integration step, seconds
	sampleRate = 600         // recorded samples per second

	pullBack    = 0.16 // how far the hand winds the wheel back
	pushStart   = 0.35 // the push begins once the wind-up ends
	releaseTime = 0.75 // the hand lets go here

	wheelInertia   = 3000.0
	bearingTorque  = 3600.0 // constant (Coulomb) friction
	airDrag        = 75.0   // torque per (rad/s)², against the motion
	stickSpeed     = 0.05   // below this the bearing's static friction can hold
	flapperInertia = 1.0
	flapperSpring  = 4740.0 // about 11 Hz
	flapperDamping = 27.5   // damping ratio near 0.2
	flapperLimit   = 1.3    // the flapper's end stops, radians either side
	contactSpring  = 322.0  // penalty stiffness of a peg against the flapper
	contactDamping = 3.0    // makes strikes inelastic

	flapperLength = float64(pointerTipY - pointerPivotY)
)

// spin is a simulated spin: the wheel's rotation and the flapper's
// deflection, sampled sampleRate times a second over the whole clip.
type spin struct {
	theta []float64
	psi   []float64
	// stopped is when the wheel last moved; rest is where it settled.
	stopped float64
	rest    float64
	// rebound is how far the flapper rocked the wheel backwards.
	rebound float64
}

// at interpolates a sampled series at t seconds.
func at(series []float64, t float64) float64 {
	x := math.Max(0, t*sampleRate)
	i := int(x)
	if i >= len(series)-1 {
		return series[len(series)-1]
	}
	f := x - float64(i)
	return series[i]*(1-f) + series[i+1]*f
}

// simulate runs one spin launched at speed rad/s from start rotation theta0
// with n pegs, one on each segment boundary.
func simulate(n int, speed, theta0 float64) spin {
	samples := DurationSeconds*sampleRate + 1
	s := spin{theta: make([]float64, samples), psi: make([]float64, samples)}
	step := 2 * math.Pi / float64(n)
	steps := int(DurationSeconds / simStep)
	every := int(math.Round(1 / (simStep * sampleRate)))

	theta, omega := theta0, 0.0
	psi, psiRate := 0.0, 0.0
	pivot := [2]float64{0, -float64(centre - pointerPivotY)}

	for i := 0; i <= steps; i++ {
		t := float64(i) * simStep
		if i%every == 0 && i/every < samples {
			s.theta[i/every] = theta
			s.psi[i/every] = psi
		}

		// Contact between the flapper and the nearest pegs.
		dir := [2]float64{math.Sin(psi), math.Cos(psi)}
		wheelTorque, flapTorque := 0.0, 0.0
		nearest := math.Round(-theta / step)
		for k := nearest - 1; k <= nearest+1; k++ {
			alpha := k*step + theta
			peg := [2]float64{pegRadius * math.Sin(alpha), -pegRadius * math.Cos(alpha)}
			rel := [2]float64{peg[0] - pivot[0], peg[1] - pivot[1]}
			along := math.Max(0, math.Min(1, (rel[0]*dir[0]+rel[1]*dir[1])/flapperLength))
			touch := [2]float64{pivot[0] + along*flapperLength*dir[0], pivot[1] + along*flapperLength*dir[1]}
			gap := [2]float64{peg[0] - touch[0], peg[1] - touch[1]}
			dist := math.Hypot(gap[0], gap[1])
			depth := pegClearance - dist
			if depth <= 0 || dist == 0 {
				continue
			}
			normal := [2]float64{gap[0] / dist, gap[1] / dist}
			pegLever := [2]float64{pegRadius * math.Cos(alpha), pegRadius * math.Sin(alpha)}
			flapLever := [2]float64{along * flapperLength * math.Cos(psi), -along * flapperLength * math.Sin(psi)}
			closing := (omega*pegLever[0]-psiRate*flapLever[0])*normal[0] + (omega*pegLever[1]-psiRate*flapLever[1])*normal[1]
			force := math.Max(0, contactSpring*depth-contactDamping*closing)
			push := [2]float64{force * normal[0], force * normal[1]}
			wheelTorque += push[0]*pegLever[0] + push[1]*pegLever[1]
			flapTorque -= push[0]*flapLever[0] + push[1]*flapLever[1]
		}

		// The flapper swings on its spring between its end stops.
		psiRate += (flapTorque - flapperSpring*psi - flapperDamping*psiRate) / flapperInertia * simStep
		psi += psiRate * simStep
		if math.Abs(psi) > flapperLimit {
			psi = math.Copysign(flapperLimit, psi)
			psiRate = 0
		}

		switch {
		case t < pushStart:
			// The hand winds the wheel back, easing in and out.
			p := t / pushStart
			theta = theta0 - pullBack*(1-math.Cos(math.Pi*p))/2
			omega = 0
		case t < releaseTime:
			// Then pushes it forward, speeding it up smoothly to launch speed.
			p := (t - pushStart) / (releaseTime - pushStart)
			target := speed * p * p * (3 - 2*p)
			theta += (omega + target) / 2 * simStep
			omega = target
		default:
			if math.Abs(omega) < stickSpeed && math.Abs(wheelTorque) < bearingTorque {
				// Static friction holds a wheel the flapper cannot move.
				omega = 0
				break
			}
			// Friction opposes the motion, or the push when the wheel is at rest.
			against := omega
			if against == 0 {
				against = wheelTorque
			}
			drag := bearingTorque + airDrag*omega*omega
			next := omega + (wheelTorque-math.Copysign(drag, against))/wheelInertia*simStep
			if next*omega < 0 && math.Abs(wheelTorque) < bearingTorque {
				next = 0 // friction stops the wheel; it does not reverse it
			}
			omega = next
			theta += omega * simStep
			if omega < 0 {
				s.rebound -= omega * simStep
			}
			if math.Abs(omega) > stickSpeed {
				s.stopped = t
			}
		}
	}
	s.rest = theta
	return s
}

// segmentUnderPointer is the segment the pointer's tip rests on at rotation
// theta, and how far into it the tip sits, from 0 to 1.
func segmentUnderPointer(n int, theta float64) (int, float64) {
	step := 2 * math.Pi / float64(n)
	disc := math.Mod(-theta, 2*math.Pi)
	if disc < 0 {
		disc += 2 * math.Pi
	}
	index := int(disc / step)
	return index % n, disc/step - float64(index)
}

// planSpin finds a launch that comes to rest naturally a little before the
// celebration starts with the tip clearly inside a segment, then turns the
// whole spin by whole segments so that segment is the winner. Turning by
// whole segments moves every peg onto another peg's place, so the motion is
// exactly the one simulated.
func planSpin(n, winner int, rng *rand.Rand) spin {
	step := 2 * math.Pi / float64(n)
	target := 4.9 + 0.6*rng.Float64()
	theta0 := rng.Float64() * step
	// Some spins end on a peg the wheel cannot push past, and rock back.
	wantRebound := n > 1 && rng.Float64() < 0.4

	// The faster the launch, the later the wheel stops; search the speed,
	// up to about four turns a second. A launch that jams on the first pegs
	// counts as too fast.
	lo, hi := 2.0, 40.0
	for range 20 {
		mid := (lo + hi) / 2
		stopped := simulate(n, mid, theta0).stopped
		if stopped < target && stopped > releaseTime+1 {
			lo = mid
		} else {
			hi = mid
		}
	}

	best, bestScore := spin{}, math.Inf(1)
	for j := range 60 {
		// Nudge the launch speed around the match until the tip settles
		// clearly inside a segment: at least minMargin pixels from a peg.
		const minMargin = 14.0
		speed := lo * (1 + 0.003*float64((j+1)/2)*float64(1-2*(j%2)))
		s := simulate(n, speed, theta0)
		_, frac := segmentUnderPointer(n, s.rest)
		margin := math.Min(frac, 1-frac) * step * pegRadius
		rebounds := s.rebound > 0.03
		score := math.Abs(s.stopped-target) / 4
		if margin < minMargin {
			score += 1 + (minMargin-margin)/minMargin
		}
		if wantRebound != rebounds {
			score += 0.5
		}
		if s.stopped > spinEnd {
			score += 10
		}
		if score < bestScore {
			best, bestScore = s, score
		}
		if margin >= minMargin && s.stopped <= spinEnd && wantRebound == rebounds {
			break
		}
	}

	landed, _ := segmentUnderPointer(n, best.rest)
	shift := float64(landed-winner) * step
	for i := range best.theta {
		best.theta[i] += shift
	}
	best.rest += shift
	return best
}
