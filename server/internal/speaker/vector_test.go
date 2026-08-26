package speaker

import (
	"math"
	"math/rand"
	"testing"
)

// synthetic builds a deterministic pseudo-embedding for a "voice", with a knob
// for how far a particular sample drifts from that voice's ideal.
func synthetic(seed int64, drift float64) Vector {
	rng := rand.New(rand.NewSource(seed))
	base := make(Vector, Dims)
	for i := range base {
		base[i] = float32(rng.NormFloat64())
	}
	if drift > 0 {
		noise := rand.New(rand.NewSource(seed * 31))
		for i := range base {
			base[i] += float32(noise.NormFloat64() * drift)
		}
	}
	return base.Normalize()
}

func TestNormalizeProducesUnitLength(t *testing.T) {
	v := synthetic(1, 0)
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if math.Abs(math.Sqrt(sum)-1) > 1e-6 {
		t.Fatalf("normalized vector length = %v, want 1", math.Sqrt(sum))
	}
}

func TestNormalizeLeavesZeroVectorAlone(t *testing.T) {
	v := make(Vector, 4).Normalize()
	for _, x := range v {
		if x != 0 {
			t.Fatalf("zero vector became %v", v)
		}
	}
}

func TestCosine(t *testing.T) {
	tests := []struct {
		name string
		a, b Vector
		want float64
	}{
		{"identical", Vector{1, 0, 0}, Vector{1, 0, 0}, 1},
		{"orthogonal", Vector{1, 0, 0}, Vector{0, 1, 0}, 0},
		{"opposite", Vector{1, 0, 0}, Vector{-1, 0, 0}, -1},
		{"unnormalized inputs", Vector{3, 0, 0}, Vector{7, 0, 0}, 1},
		{"length mismatch", Vector{1, 0}, Vector{1, 0, 0}, 0},
		{"empty", Vector{}, Vector{}, 0},
		{"zero vector", Vector{0, 0, 0}, Vector{1, 0, 0}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Cosine(tc.a, tc.b); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("Cosine() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCentroidAveragesSamplesOfOneVoice(t *testing.T) {
	var samples []Vector
	for i := 0; i < 5; i++ {
		samples = append(samples, synthetic(42, 0.25+float64(i)*0.02))
	}
	centroid, err := Centroid(samples)
	if err != nil {
		t.Fatalf("Centroid() error: %v", err)
	}

	ideal := synthetic(42, 0)
	// The average of noisy samples must sit closer to the true voice than any
	// single sample does — that is the whole point of enrolling five of them.
	worst := 1.0
	for _, sample := range samples {
		if s := Cosine(sample, ideal); s < worst {
			worst = s
		}
	}
	if got := Cosine(centroid, ideal); got <= worst {
		t.Fatalf("centroid similarity %v is no better than the worst sample %v", got, worst)
	}
}

func TestCentroidRejectsEmptyInput(t *testing.T) {
	if _, err := Centroid(nil); err == nil {
		t.Fatal("expected an error for an empty sample set")
	}
}

func TestCentroidIgnoresWrongLengthVectors(t *testing.T) {
	centroid, err := Centroid([]Vector{{1, 0, 0}, {0, 1}, {1, 0, 0}})
	if err != nil {
		t.Fatalf("Centroid() error: %v", err)
	}
	if len(centroid) != 3 {
		t.Fatalf("centroid has %d dims, want 3", len(centroid))
	}
}

func TestMeanSitsBetweenItsInputs(t *testing.T) {
	a, b := synthetic(1, 0), synthetic(2, 0)
	mean := Mean(a, b)
	if sa, sb := Cosine(mean, a), Cosine(mean, b); math.Abs(sa-sb) > 1e-6 {
		t.Fatalf("mean is not equidistant: %v vs %v", sa, sb)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	original := synthetic(7, 0)
	decoded, err := Decode(Encode(original))
	if err != nil {
		t.Fatalf("Decode() error: %v", err)
	}
	if len(decoded) != len(original) {
		t.Fatalf("decoded %d dims, want %d", len(decoded), len(original))
	}
	for i := range original {
		if decoded[i] != original[i] {
			t.Fatalf("dim %d: got %v, want %v", i, decoded[i], original[i])
		}
	}
}

func TestDecodeRejectsRaggedBlob(t *testing.T) {
	if _, err := Decode([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected an error for a blob that is not a whole number of float32s")
	}
}

func TestDecodeEmptyBlob(t *testing.T) {
	v, err := Decode(nil)
	if err != nil || len(v) != 0 {
		t.Fatalf("Decode(nil) = %v, %v; want empty, nil", v, err)
	}
}
