package speaker

import "fmt"

// Enrollment is one recorded sample belonging to a speaker.
type Enrollment struct {
	ID         int64   `json:"id"`
	SpeakerID  int64   `json:"speaker_id"`
	Embedding  Vector  `json:"-"`
	DurationS  float64 `json:"duration_s"`
	Label      string  `json:"label"`
	CreatedAt  string  `json:"created_at"`
	Similarity float64 `json:"similarity,omitempty"`
}

// SampleCheck is the verdict on a freshly recorded enrollment sample.
type SampleCheck struct {
	Similarity float64 `json:"similarity"`
	Accepted   bool    `json:"accepted"`
	Reason     string  `json:"reason,omitempty"`
}

// CheckSample scores a new sample against the samples collected so far. The
// first sample is always accepted; later ones must resemble what came before,
// which is how a cough or a slammed door gets caught during enrollment.
func CheckSample(sample Vector, existing []Vector, minSimilarity float64) SampleCheck {
	if len(sample) == 0 {
		return SampleCheck{Accepted: false, Reason: "empty embedding"}
	}
	if len(existing) == 0 {
		return SampleCheck{Similarity: 1, Accepted: true}
	}
	centroid, err := Centroid(existing)
	if err != nil {
		return SampleCheck{Accepted: false, Reason: err.Error()}
	}
	similarity := Cosine(sample, centroid)
	if similarity < minSimilarity {
		return SampleCheck{
			Similarity: similarity,
			Accepted:   false,
			Reason: fmt.Sprintf(
				"sample does not match the other recordings (%.2f < %.2f) — record it again",
				similarity, minSimilarity),
		}
	}
	return SampleCheck{Similarity: similarity, Accepted: true}
}
