package fingerprint

import (
	"math"
	"math/bits"
	"math/cmplx"
)

// transform computes the chroma of a frame: Hamming window, fast Fourier transform, energy of each
// bin added to the class of its note. Its buffers are reused for every frame.
type transform struct {
	window  []float64
	twiddle []complex128
	reverse []int
	// class gives the pitch class (0 is C) of each bin, -1 outside the band we keep.
	class []int
	buf   []complex128
}

func newTransform() *transform {
	t := &transform{
		window:  make([]float64, frameSize),
		twiddle: make([]complex128, frameSize/2),
		reverse: make([]int, frameSize),
		class:   make([]int, frameSize/2),
		buf:     make([]complex128, frameSize),
	}
	for i := range t.window {
		t.window[i] = 0.54 - 0.46*math.Cos(2*math.Pi*float64(i)/float64(frameSize-1))
	}
	for i := range t.twiddle {
		t.twiddle[i] = cmplx.Exp(complex(0, -2*math.Pi*float64(i)/float64(frameSize)))
	}
	shift := bits.UintSize - bits.Len(uint(frameSize-1))
	for i := range t.reverse {
		t.reverse[i] = int(bits.Reverse(uint(i)) >> shift)
	}
	for k := range t.class {
		f := float64(k) * SampleRate / frameSize
		if f < minFreq || f > maxFreq {
			t.class[k] = -1
			continue
		}
		// MIDI number of the nearest note (69 is A at 440 Hz), then its class.
		midi := int(math.Round(69 + 12*math.Log2(f/440)))
		t.class[k] = midi % 12
	}
	return t
}

// chroma returns the energy of the 12 pitch classes of a frame, and whether the frame is silent.
func (t *transform) chroma(frame []float64) ([12]float64, bool) {
	var c [12]float64
	var power float64
	for i, v := range frame {
		power += v * v
		t.buf[t.reverse[i]] = complex(v*t.window[i], 0)
	}
	if math.Sqrt(power/float64(len(frame))) < silenceRMS {
		return c, true
	}
	t.fft()
	for k, class := range t.class {
		if class >= 0 {
			v := t.buf[k]
			c[class] += real(v)*real(v) + imag(v)*imag(v)
		}
	}
	return c, false
}

// fft transforms buf in place (iterative radix 2; buf is already in bit-reversed order).
func (t *transform) fft() {
	n := len(t.buf)
	for size := 2; size <= n; size <<= 1 {
		half, step := size/2, n/size
		for start := 0; start < n; start += size {
			for k := range half {
				w := t.twiddle[k*step]
				a, b := t.buf[start+k], w*t.buf[start+k+half]
				t.buf[start+k], t.buf[start+k+half] = a+b, a-b
			}
		}
	}
}
