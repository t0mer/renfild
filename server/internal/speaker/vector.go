// Package speaker turns voice embeddings into identities: vector maths,
// enrollment centroids and the matching rules that decide who is talking.
package speaker

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Dims is the ECAPA-TDNN embedding size produced by the embedder sidecar.
const Dims = 192

// Vector is a speaker embedding. Vectors handed to Cosine are expected to be
// L2-normalized; Normalize makes that so.
type Vector []float32

// Normalize returns a unit-length copy of v. A zero vector is returned as-is.
func (v Vector) Normalize() Vector {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		return append(Vector(nil), v...)
	}
	out := make(Vector, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / norm)
	}
	return out
}

// Cosine returns the cosine similarity of two vectors, or 0 if their lengths
// differ or either is degenerate.
func Cosine(a, b Vector) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Centroid returns the L2-normalized mean of the given vectors — a speaker's
// voice print. Vectors of the wrong length are ignored.
func Centroid(vectors []Vector) (Vector, error) {
	if len(vectors) == 0 {
		return nil, fmt.Errorf("no vectors to average")
	}
	dims := len(vectors[0])
	sum := make([]float64, dims)
	used := 0
	for _, v := range vectors {
		if len(v) != dims {
			continue
		}
		for i, x := range v {
			sum[i] += float64(x)
		}
		used++
	}
	if used == 0 {
		return nil, fmt.Errorf("no vectors of consistent length")
	}
	mean := make(Vector, dims)
	for i, s := range sum {
		mean[i] = float32(s / float64(used))
	}
	return mean.Normalize(), nil
}

// Mean returns the L2-normalized average of two vectors. It is used when the
// wake snapshot alone is not decisive and the command audio gets a vote.
func Mean(a, b Vector) Vector {
	if len(a) != len(b) {
		if len(a) > 0 {
			return a
		}
		return b
	}
	out := make(Vector, len(a))
	for i := range a {
		out[i] = (a[i] + b[i]) / 2
	}
	return out.Normalize()
}

// Encode serialises a vector as little-endian float32, the on-disk BLOB format.
func Encode(v Vector) []byte {
	buf := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(x))
	}
	return buf
}

// Decode parses a BLOB produced by Encode.
func Decode(buf []byte) (Vector, error) {
	if len(buf)%4 != 0 {
		return nil, fmt.Errorf("embedding blob of %d bytes is not a whole number of float32s", len(buf))
	}
	out := make(Vector, len(buf)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return out, nil
}
