// Test out the AHRS code in ahrs/ahrs.go.
// Define a flight path/attitude in code, and then synthesize the matching GPS, gyro, accel (and other) data
// Add some noise if desired.
// Then see if the AHRS code can replicate the "true" attitude given the noisy and limited input data
package main

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sort"

	"github.com/skelterjohn/go.matrix"
	"github.com/westphae/goflying/ahrs"
)

const (
	pi = math.Pi
)

// Situation defines a scenario by piecewise-linear interpolation
type Situation struct {
	t                  []float64 // times for situation, s
	u1, u2, u3         []float64 // airspeed, kts, aircraft frame [F/B, R/L, and U/D]
	phi, theta, psi    []float64 // attitude, rad [roll R/L, pitch U/D, heading N->E->S->W]
	phi0, theta0, psi0 []float64 // base attitude, rad [adjust for position of stratux on glareshield]
	v1, v2, v3         []float64 // windspeed, kts, earth frame [N/S, E/W, and U/D]
	m1, m2, m3         []float64 // magnetometer reading
}

// ToQuaternion calculates the 0,1,2,3 components of the rotation quaternion
// corresponding to the Tait-Bryan angles phi, theta, psi
func ToQuaternion(phi, theta, psi float64) (float64, float64, float64, float64) {
	phi = -phi            // We want psi positive to mean a roll to the right
	psi = psi - pi/2 // We want psi=0 means north, psi=Pi/2 means east
	cphi := math.Cos(phi / 2)
	sphi := math.Sin(phi / 2)
	ctheta := math.Cos(theta / 2)
	stheta := math.Sin(theta / 2)
	cpsi := math.Cos(psi / 2)
	spsi := math.Sin(psi / 2)

	q0 := cphi*ctheta*cpsi - sphi*stheta*spsi
	q1 := sphi*ctheta*cpsi + cphi*stheta*spsi
	q2 := cphi*stheta*cpsi - sphi*ctheta*spsi
	q3 := cphi*ctheta*spsi + sphi*stheta*cpsi
	return q0, q1, q2, q3
}

// FromQuaternion calculates the Tait-Bryan angles phi, theta, phi corresponding to
// the quaternion
func FromQuaternion(q0, q1, q2, q3 float64) (float64, float64, float64) {
	phi := math.Atan2(-2*(q0*q1-q2*q3), q0*q0-q1*q1-q2*q2+q3*q3)
	theta := math.Asin(2 * (q0*q2 + q3*q1) / math.Sqrt(q0*q0+q1*q1+q2*q2+q3*q3))
	psi := pi/2 + math.Atan2(2*(q0*q3-q1*q2), q0*q0+q1*q1-q2*q2-q3*q3)
	if psi < -1e-4 {
		psi += 2 * pi
	}
	return phi, theta, psi
}

// Interpolate an ahrs.State from a Situation definition at a given time
func (s *Situation) interpolate(t float64) (ahrs.State, error) {
	if t < s.t[0] || t > s.t[len(s.t)-1] {
		return ahrs.State{}, errors.New("requested time is outside of scenario")
	}
	ix := 0
	if t > s.t[0] {
		ix = sort.SearchFloat64s(s.t, t) - 1
	}

	f := (s.t[ix+1] - t) / (s.t[ix+1] - s.t[ix])
	e0, e1, e2, e3 := ToQuaternion(
		f*s.phi[ix]+(1-f)*s.phi[ix+1],
		f*s.theta[ix]+(1-f)*s.theta[ix+1],
		f*s.psi[ix]+(1-f)*s.psi[ix+1])
	ee := math.Sqrt(e0*e0 + e1*e1 + e2*e2 + e3*e3)
	f0, f1, f2, f3 := ToQuaternion(
		f*s.phi0[ix]+(1-f)*s.phi0[ix+1],
		f*s.theta0[ix]+(1-f)*s.theta0[ix+1],
		f*s.psi0[ix]+(1-f)*s.psi0[ix+1])
	ff := math.Sqrt(f0*f0 + f1*f1 + f2*f2 + f3*f3)

	return ahrs.State{
		U1: f*s.u1[ix] + (1-f)*s.u1[ix+1],
		U2: f*s.u2[ix] + (1-f)*s.u2[ix+1],
		U3: f*s.u3[ix] + (1-f)*s.u3[ix+1],
		E0: e0 / ee,
		E1: e1 / ee,
		E2: e2 / ee,
		E3: e3 / ee,
		F0: f0 / ff,
		F1: f1 / ff,
		F2: f2 / ff,
		F3: f3 / ff,
		V1: f*s.v1[ix] + (1-f)*s.v1[ix+1],
		V2: f*s.v2[ix] + (1-f)*s.v2[ix+1],
		V3: f*s.v3[ix] + (1-f)*s.v3[ix+1],
		M1: f*s.m1[ix] + (1-f)*s.m1[ix+1],
		M2: f*s.m2[ix] + (1-f)*s.m2[ix+1],
		M3: f*s.m3[ix] + (1-f)*s.m3[ix+1],
		T:  uint32(t*1000 + 0.5), // easy rounding for uint
		M:  matrix.DenseMatrix{},
	}, nil
}

// Determine time derivative of an ahrs.State from a Situation definition at a given time
func (s *Situation) derivative(t float64) (ahrs.State, error) {
	if t < s.t[0] || t > s.t[len(s.t)-1] {
		return ahrs.State{}, errors.New("requested time is outside of scenario")
	}

	var t0, t1, ddt float64
	ddt = 0.001
	t0, t1 = t, t+ddt
	if t1 > s.t[len(s.t)-1] {
		t1 = s.t[len(s.t)-1]
		t0 = t1 - ddt
	}

	s0, _ := s.interpolate(t0)
	s1, _ := s.interpolate(t1)

	return ahrs.State{
		U1: (s1.U1 - s0.U1) / ddt,
		U2: (s1.U2 - s0.U2) / ddt,
		U3: (s1.U3 - s0.U3) / ddt,
		E0: (s1.E0 - s0.E0) / ddt,
		E1: (s1.E1 - s0.E1) / ddt,
		E2: (s1.E2 - s0.E2) / ddt,
		E3: (s1.E3 - s0.E3) / ddt,
		F0: (s1.F0 - s0.F0) / ddt,
		F1: (s1.F1 - s0.F1) / ddt,
		F2: (s1.F2 - s0.F2) / ddt,
		F3: (s1.F3 - s0.F3) / ddt,
		V1: (s1.V1 - s0.V1) / ddt,
		V2: (s1.V2 - s0.V2) / ddt,
		V3: (s1.V3 - s0.V3) / ddt,
		M1: (s1.M1 - s0.M1) / ddt,
		M2: (s1.M2 - s0.M2) / ddt,
		M3: (s1.M3 - s0.M3) / ddt,
		T:  uint32(t*1000 + 0.5), // easy rounding for uint
		M:  matrix.DenseMatrix{},
	}, nil
}

// Determine ahrs.Control variables from a Situation definition at a given time
func (s *Situation) control(t float64) (ahrs.Control, error) {
	x, erri := s.interpolate(t)
	dx, errd := s.derivative(t)
	if erri != nil || errd != nil {
		return ahrs.Control{}, errors.New("requested time is outside of scenario")
	}

	// f fragments to reverse-rotate by f (airplane frame to sensor frame)
	f11 := 2 * (+x.F0*x.F0 + x.F1*x.F1 - 0.5)
	f12 := 2 * (+x.F0*x.F3 + x.F1*x.F2)
	f13 := 2 * (-x.F0*x.F2 + x.F1*x.F3)
	f21 := 2 * (-x.F0*x.F3 + x.F2*x.F1)
	f22 := 2 * (+x.F0*x.F0 + x.F2*x.F2 - 0.5)
	f23 := 2 * (+x.F0*x.F1 + x.F2*x.F3)
	f31 := 2 * (+x.F0*x.F2 + x.F3*x.F1)
	f32 := 2 * (-x.F0*x.F1 + x.F3*x.F2)
	f33 := 2 * (+x.F0*x.F0 + x.F3*x.F3 - 0.5)

	h1 := 2 * (dx.E1*x.E0 - dx.E0*x.E1 + dx.E3*x.E2 - dx.E2*x.E3)
	h2 := 2 * (dx.E2*x.E0 - dx.E3*x.E1 - dx.E0*x.E2 + dx.E1*x.E3)
	h3 := 2 * (dx.E3*x.E0 + dx.E2*x.E1 - dx.E1*x.E2 - dx.E0*x.E3)

	y1 := -2*(+x.E0*x.E2 + x.E3*x.E1)       + (-dx.U1 + h2*x.U3 - h3*x.U2)/ahrs.G
	y2 := -2*(-x.E0*x.E1 + x.E3*x.E2)       + (-dx.U2 + h3*x.U1 - h1*x.U3)/ahrs.G
	y3 := -2*(+x.E0*x.E0 + x.E3*x.E3 - 0.5) + (-dx.U3 + h1*x.U2 - h2*x.U1)/ahrs.G

	c := ahrs.Control{
		H1: h1*f11 + h2*f12 + h3*f13,
		H2: h1*f21 + h2*f22 + h3*f23,
		H3: h1*f31 + h2*f32 + h3*f33,
		A1: y1*f11 + y2*f12 + y3*f13,
		A2: y1*f21 + y2*f22 + y3*f23,
		A3: y1*f31 + y2*f32 + y3*f33,
		T:  uint32(t*1000 + 0.5),
	}
	return c, nil
}

// Determine ahrs.Measurement variables from a Situation definition at a given time
func (s *Situation) measurement(t float64) (ahrs.Measurement, error) {
	if t < s.t[0] || t > s.t[len(s.t)-1] {
		return ahrs.Measurement{}, errors.New("requested time is outside of scenario")
	}
	x, _ := s.interpolate(t)

	m := ahrs.Measurement{
		W1: x.V1 +
			x.U1*2*(+x.E0*x.E0 + x.E1*x.E1 - 0.5) +
			x.U2*2*(+x.E0*x.E3 + x.E1*x.E2) +
			x.U3*2*(-x.E0*x.E2 + x.E1*x.E3),
		W2: x.V2 +
			x.U1*2*(-x.E0*x.E3 + x.E2*x.E1) +
			x.U2*2*(+x.E0*x.E0 + x.E2*x.E2 - 0.5) +
			x.U3*2*(+x.E0*x.E1 + x.E2*x.E3),
		W3: x.V3 +
			x.U1*2*(+x.E0*x.E2 + x.E3*x.E1) +
			x.U2*2*(-x.E0*x.E1 + x.E3*x.E2) +
			x.U3*2*(+x.E0*x.E0 + x.E3*x.E3 - 0.5),
		M1: x.M1*2*(+x.E0*x.E0 + x.E1*x.E1 - 0.5) +
			x.M2*2*(-x.E0*x.E3 + x.E1*x.E2) +
			x.M3*2*(+x.E0*x.E2 + x.E1*x.E3),
		M2: x.M1*2*(+x.E0*x.E3 + x.E2*x.E1) +
			x.M2*2*(+x.E0*x.E0 + x.E2*x.E2 - 0.5) +
			x.M3*2*(-x.E0*x.E1 + x.E2*x.E3),
		M3: x.M1*2*(-x.E0*x.E2 + x.E3*x.E1) +
			x.M2*2*(+x.E0*x.E1 + x.E3*x.E2) +
			x.M3*2*(+x.E0*x.E0 + x.E3*x.E3 - 0.5),
		U1: x.U1,
		U2: x.U2,
		U3: x.U3,
		T:  uint32(t*1000 + 0.5),
	}
	return m, nil
}

// addControlNoise adds Gaussian sensor noise to the control struct
// gyro noise stdev is in deg/s
// accel noise stdev is in kt/s
func addControlNoise(c *ahrs.Control, gn, an float64) {
	if gn>0 {
		c.H1 += gn * rand.NormFloat64()
		c.H2 += gn * rand.NormFloat64()
		c.H3 += gn * rand.NormFloat64()
	}
	if an>0 {
		c.A1 += an * rand.NormFloat64()
		c.A2 += an * rand.NormFloat64()
		c.A3 += an * rand.NormFloat64()
	}
}

// addMeasurementNoise adds Gaussian sensor noise to the measurement struct
// gps noise stdev is in kt
// airspeed noise is in kt
// magnetometer noise is in uH
func addMeasurementNoise(m *ahrs.Measurement, sn, un, mn float64) {
	if sn>0 {
		m.W1 += sn * rand.NormFloat64()
		m.W2 += sn * rand.NormFloat64()
		m.W3 += sn * rand.NormFloat64()
	}
	if un>0 {
		m.U1 += un * rand.NormFloat64()
		m.U2 += un * rand.NormFloat64()
		m.U3 += un * rand.NormFloat64()
	}
	if mn>0 {
		m.M1 += mn * rand.NormFloat64()
		m.M2 += mn * rand.NormFloat64()
		m.M3 += mn * rand.NormFloat64()
	}
}

// Data to define a piecewise-linear turn, with entry and exit
var airspeed = 120.0                                            // Nice airspeed for maneuvers, kts
var bank = math.Atan((2 * pi * airspeed) / (ahrs.G * 120)) // Bank angle for std rate turn at given airspeed
var mush = -airspeed*math.Sin(pi/90)/math.Cos(bank)	// Mush in a turn to maintain altitude
var sitTurnDef = Situation{                                     // start, initiate roll-in, end roll-in, initiate roll-out, end roll-out, end
	t:      []float64{0, 10, 15, 255, 260, 270},
	u1:     []float64{airspeed, airspeed, airspeed, airspeed, airspeed, airspeed},
	u2:     []float64{0, 0, 0, 0, 0, 0},
	u3:     []float64{0, 0, mush, mush, 0, 0},
	phi:    []float64{0, 0, bank, bank, 0, 0},
	theta:  []float64{0, 0, pi/90, pi/90, 0, 0},
	psi:    []float64{0, 0, 0, 4*pi, 4*pi, 4*pi},
	phi0:   []float64{0, 0, 0, 0, 0, 0},
	theta0: []float64{0, 0, 0, 0, 0, 0},
	psi0:   []float64{pi/2, pi/2, pi/2, pi/2, pi/2, pi/2},
	v1:     []float64{3, 3, 3, 3, 3, 3},
	v2:     []float64{4, 4, 4, 4, 4, 4},
	v3:     []float64{0, 0, 0, 0, 0, 0},
	m1:     []float64{0, 0, 0, 0, 0, 0},
	m2:     []float64{0, 0, 0, 0, 0, 0},
	m3:     []float64{0, 0, 0, 0, 0, 0},
}

func main() {
	// Handle some shell arguments
	var (
		dt, gyroNoise, accelNoise, gpsNoise  float64
	)

	const (
		defaultDt = 0.1
		dtUsage = "Kalman filter update period, seconds"
		defaultGyroNoise = 0.0
		gyroNoiseUsage = "Amount of noise to add to gyro measurements, deg/s"
		defaultAccelNoise = 0.0
		accelNoiseUsage = "Amount of noise to add to accel measurements, G"
		defaultGPSNoise = 0.0
		gpsNoiseUsage = "Amount of noise to add to GPS speed measurements, kt"
	)

	flag.Float64Var(&dt, "dt", defaultDt, dtUsage)
	flag.Float64Var(&gyroNoise, "gyro-noise", defaultGyroNoise, gyroNoiseUsage)
	flag.Float64Var(&gyroNoise, "g", defaultGyroNoise, gyroNoiseUsage)
	flag.Float64Var(&accelNoise, "accel-noise", defaultAccelNoise, accelNoiseUsage)
	flag.Float64Var(&accelNoise, "a", defaultAccelNoise, accelNoiseUsage)
	flag.Float64Var(&gpsNoise, "gps-noise", defaultGPSNoise, gpsNoiseUsage)
	flag.Float64Var(&gpsNoise, "s", defaultGPSNoise, gpsNoiseUsage)
	flag.Parse()

	// Files to save data to for analysis
	fActual, err := os.Create("k_state.csv")
	if err != nil {
		panic(err)
	}
	defer fActual.Close()
	fmt.Fprint(fActual, "T,Ux,Uy,Uz,Phi,Theta,Psi,Vx,Vy,Vz,Mx,My,Mz\n")
	fKalman, err := os.Create("k_kalman.csv")
	if err != nil {
		panic(err)
	}
	defer fKalman.Close()
	fmt.Fprint(fKalman, "T,Ux,Uy,Uz,Phi,Theta,Psi,Vx,Vy,Vz,Mx,My,Mz\n")
	fPredict, err := os.Create("k_predict.csv")
	if err != nil {
		panic(err)
	}
	defer fPredict.Close()
	fmt.Fprint(fPredict, "T,Ux,Uy,Uz,Phi,Theta,Psi,Vx,Vy,Vz,Mx,My,Mz\n")
	fVar, err := os.Create("k_var.csv")
	if err != nil {
		panic(err)
	}
	defer fVar.Close()
	fmt.Fprint(fVar, "T,Ux,Uy,Uz,Phi,Theta,Psi,Vx,Vy,Vz,Mx,My,Mz\n")
	fControl, err := os.Create("k_control.csv")
	if err != nil {
		panic(err)
	}
	defer fControl.Close()
	fmt.Fprint(fControl, "T,P,Q,R,Ax,Ay,Az\n")
	fMeas, err := os.Create("k_meas.csv")
	if err != nil {
		panic(err)
	}
	defer fMeas.Close()
	fmt.Fprint(fMeas, "T,Wx,Wy,Wz,Mx,My,Mz,Ux,Uy,Uz\n")

	s := ahrs.X0 // Initialize Kalman with a sensible starting state
	s.Calibrate()
	fmt.Println("Running Simulation")
	for t := sitTurnDef.t[0]; t < sitTurnDef.t[len(sitTurnDef.t)-1]; t += dt {
		// Peek behind the curtain: the "actual" state, which the algorithm doesn't know
		s0, err := sitTurnDef.interpolate(t)
		if err != nil {
			fmt.Printf("Error interpolating at time %f: %s", t, err.Error())
			panic(err)
		}
		phi, theta, psi := FromQuaternion(s0.E0, s0.E1, s0.E2, s0.E3)
		fmt.Fprintf(fActual, "%f,%f,%f,%f,%f,%f,%f,%f,%f,%f,%f,%f,%f\n",
			float64(s0.T)/1000, s0.U1, s0.U2, s0.U3, phi, theta, psi,
			s0.V1, s0.V2, s0.V3, s0.M1, s0.M2, s0.M3)

		// Take control "measurements"
		c, err := sitTurnDef.control(t)
		if err != nil {
			fmt.Printf("Error calculating control value at time %f: %s", t, err.Error())
			panic(err)
		}
		addControlNoise(&c, gyroNoise*pi/180, accelNoise)
		fmt.Fprintf(fControl, "%f,%f,%f,%f,%f,%f,%f\n",
			float64(c.T)/1000, -c.H1, c.H2, c.H3, c.A1, c.A2, c.A3)

		// Predict stage of Kalman filter
		s.Predict(c, ahrs.VX)
		phi, theta, psi = FromQuaternion(s.E0, s.E1, s.E2, s.E3)
		fmt.Fprintf(fPredict, "%f,%f,%f,%f,%f,%f,%f,%f,%f,%f,%f,%f,%f\n",
			float64(s.T)/1000, s.U1, s.U2, s.U3, phi, theta, psi,
			s.V1, s.V2, s.V3, s.M1, s.M2, s.M3)

		// Take sensor measurements
		m, err := sitTurnDef.measurement(t)
		if err != nil {
			fmt.Printf("Error calculating measurement value at time %f: %s", t, err.Error())
			panic(err)
		}
		addMeasurementNoise(&m, gpsNoise, 0, 0)
		fmt.Fprintf(fMeas, "%f,%f,%f,%f,%f,%f,%f,%f,%f,%f\n",
			float64(m.T)/1000, m.W1, m.W2, m.W3, m.M1, m.M2, m.M3, m.U1, m.U2, m.U3)

		// Update stage of Kalman filter
		s.Update(m, ahrs.VM)
		phi, theta, psi = FromQuaternion(s.E0, s.E1, s.E2, s.E3)
		fmt.Fprintf(fKalman, "%f,%f,%f,%f,%f,%f,%f,%f,%f,%f,%f,%f,%f\n",
			float64(s.T)/1000, s.U1, s.U2, s.U3, phi, theta, psi,
			s.V1, s.V2, s.V3, s.M1, s.M2, s.M3)
		fmt.Fprintf(fVar, "%f,%f,%f,%f,%f,%f,%f,%f,%f,%f,%f,%f,%f\n",
			float64(s.T)/1000, math.Sqrt(s.M.Get(0, 0)), math.Sqrt(s.M.Get(1, 1)), math.Sqrt(s.M.Get(2, 2)),
			//TODO: Calculate variance of Tait-Bryan from Quaternions
			math.Sqrt(s.M.Get(3, 3)), math.Sqrt(s.M.Get(4, 4)), math.Sqrt(s.M.Get(5, 5)),
			math.Sqrt(s.M.Get(7, 7)), math.Sqrt(s.M.Get(8, 8)), math.Sqrt(s.M.Get(9, 9)),
			math.Sqrt(s.M.Get(10, 10)), math.Sqrt(s.M.Get(11, 11)), math.Sqrt(s.M.Get(12, 12)),
		)
	}

	// Run analysis web server
	fmt.Println("Serving charts")
	http.Handle("/", http.FileServer(http.Dir("./")))
	http.ListenAndServe(":8080", nil)
}
