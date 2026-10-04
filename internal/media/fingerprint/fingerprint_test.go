package fingerprint

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// melody makes a sequence of notes from a scale, each 0.2 to 0.6 s long with two harmonics and an
// envelope. That is close enough to music for different passages to sound a bit alike, as they do
// in real life.
func melody(seed uint64, d time.Duration) []float64 {
	r := rand.New(rand.NewPCG(seed, 1))
	scale := []float64{0, 2, 4, 5, 7, 9, 11} // major scale, in semitones
	total := int(d.Seconds() * SampleRate)
	out := make([]float64, 0, total)
	for len(out) < total {
		semitone := scale[r.IntN(len(scale))] + 12*float64(r.IntN(3))
		f := 220 * math.Pow(2, semitone/12)
		n := int((0.2 + 0.4*r.Float64()) * SampleRate)
		for i := range n {
			t := float64(i) / SampleRate
			env := math.Min(1, math.Min(t/0.02, float64(n-i)/SampleRate/0.05))
			v := math.Sin(2*math.Pi*f*t) + 0.5*math.Sin(4*math.Pi*f*t) + 0.25*math.Sin(6*math.Pi*f*t)
			out = append(out, 0.2*env*v)
		}
	}
	return out[:total]
}

// pcm encodes the audio in the format of Args, with noise specific to each "encode".
func pcm(samples []float64, noiseSeed uint64, gain float64) []byte {
	r := rand.New(rand.NewPCG(noiseSeed, 2))
	out := make([]byte, 0, 2*len(samples))
	for _, v := range samples {
		v = gain*v + 0.001*r.NormFloat64()
		out = binary.LittleEndian.AppendUint16(out, uint16(int16(math.Max(-1, math.Min(1, v))*32767)))
	}
	return out
}

func compute(t *testing.T, samples []float64, seed uint64, gain float64) Print {
	t.Helper()
	p, err := Compute(bytes.NewReader(pcm(samples, seed, gain)), 0)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func near(got, want time.Duration) bool { return (got - want).Abs() <= 400*time.Millisecond }

// The same 30 s intro, placed at different times (not a multiple of the hop) in two episodes and
// encoded with different noise and volume.
func TestCommonFindsSharedPassage(t *testing.T) {
	intro := melody(1, 30*time.Second)
	a := slices.Concat(melody(10, 20*time.Second), intro, melody(11, 40*time.Second))
	offset := 55536 // 5.037 s: not a multiple of the hop
	b := slices.Concat(melody(20, 6*time.Second)[:offset], intro, melody(21, 50*time.Second))
	pa, pb := compute(t, a, 100, 1), compute(t, b, 200, 0.8)
	ra, rb, ok := Common(pa, pb, 10*time.Second)
	if !ok {
		t.Fatal("shared intro not found")
	}
	bStart := time.Duration(offset) * time.Second / SampleRate
	if !near(ra.Start, 20*time.Second) || !near(ra.End, 50*time.Second) || !near(rb.Start, bStart) || !near(rb.End, bStart+30*time.Second) {
		t.Errorf("ranges: A %v -> %v, B %v -> %v", ra.Start, ra.End, rb.Start, rb.End)
	}
}

// Different music, even in the same scale, has nothing in common.
func TestCommonRejectsDifferentMusic(t *testing.T) {
	pa := compute(t, melody(30, 3*time.Minute), 1, 1)
	pb := compute(t, melody(31, 3*time.Minute), 2, 1)
	if ra, rb, ok := Common(pa, pb, 5*time.Second); ok {
		t.Errorf("made up a shared range: %v, %v", ra, rb)
	}
}

// Two silences (or near silences) do not make an intro.
func TestCommonIgnoresSilence(t *testing.T) {
	quiet := make([]float64, 60*SampleRate)
	pa, pb := compute(t, quiet, 1, 1), compute(t, quiet, 2, 1)
	if !slices.Contains(pa.Silent, true) || slices.Contains(pa.Silent, false) {
		t.Fatalf("silence not detected: %v", pa.Silent[:5])
	}
	if _, _, ok := Common(pa, pb, 5*time.Second); ok {
		t.Error("two silences taken for a shared range")
	}
}

// The start in the file and its offset show up in the ranges, and the length is that of the audio
// read.
func TestPrintTimes(t *testing.T) {
	intro := melody(5, 12*time.Second)
	pa, err := Compute(bytes.NewReader(pcm(slices.Concat(intro, melody(6, 8*time.Second)), 1, 1)), 0)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := Compute(bytes.NewReader(pcm(slices.Concat(intro, melody(7, 8*time.Second)), 2, 1)), 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if pa.Length != 20*time.Second || len(pa.Codes) != 1+(20*SampleRate-frameSize)/hop {
		t.Errorf("length %v, %d frames", pa.Length, len(pa.Codes))
	}
	ra, rb, ok := Common(pa, pb, 5*time.Second)
	if !ok || ra.Start != 0 || rb.Start != 90*time.Second || !near(ra.End, 12*time.Second) || !near(rb.End, 102*time.Second) {
		t.Errorf("ranges: %v, %v, %v", ra, rb, ok)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	p := compute(t, slices.Concat(melody(8, 5*time.Second), make([]float64, 3*SampleRate)), 1, 1)
	p.Start = 42 * time.Second
	data, err := p.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var back Print
	if err := back.UnmarshalBinary(data); err != nil {
		t.Fatal(err)
	}
	if back.Start != p.Start || back.Length != p.Length || !slices.Equal(back.Codes, p.Codes) || !slices.Equal(back.Silent, p.Silent) {
		t.Error("fingerprint differs after a round trip")
	}
	if err := back.UnmarshalBinary(data[:len(data)-1]); err == nil {
		t.Error("truncated fingerprint accepted")
	}
	if err := back.UnmarshalBinary(append([]byte("LFP0"), data[4:]...)); err == nil {
		t.Error("fingerprint from another algorithm accepted")
	}
}

func TestArgs(t *testing.T) {
	got := Args("/m/a b.mkv", 2, 90*time.Second, 10*time.Minute)
	want := []string{"-ss", "90.000", "-t", "600.000", "-i", "file:/m/a b.mkv", "-map", "0:2"}
	for i := 0; i+1 < len(want); i += 2 {
		if j := slices.Index(got, want[i]); j < 0 || got[j+1] != want[i+1] {
			t.Errorf("%s: %v", want[i], got)
		}
	}
}

func BenchmarkCompute10Minutes(b *testing.B) {
	data := pcm(melody(1, 10*time.Minute), 1, 1)
	for b.Loop() {
		if _, err := Compute(bytes.NewReader(data), 0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCommon10Minutes(b *testing.B) {
	pa, _ := Compute(bytes.NewReader(pcm(melody(1, 10*time.Minute), 1, 1)), 0)
	pb, _ := Compute(bytes.NewReader(pcm(melody(2, 10*time.Minute), 2, 1)), 0)
	for b.Loop() {
		Common(pa, pb, 15*time.Second)
	}
}
