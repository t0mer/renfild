package speaker

import "time"

// Unknown is the speaker name reported when no centroid clears its threshold.
const Unknown = "unknown"

// Record is an enrolled speaker as stored in the database.
type Record struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Role      Role      `json:"role"`
	Centroid  Vector    `json:"-"`
	Threshold *float64  `json:"threshold,omitempty"`
	Samples   int       `json:"samples"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EffectiveThreshold is the speaker's own threshold if set, else the default.
func (r Record) EffectiveThreshold(def float64) float64 {
	if r.Threshold != nil {
		return *r.Threshold
	}
	return def
}

// Identity is the outcome of matching one embedding against every enrolled
// speaker. The runner-up is carried along because the gap between the top two
// scores is what tells you whether a threshold needs tuning.
type Identity struct {
	Name          string  `json:"name"`
	Role          Role    `json:"role"`
	Score         float64 `json:"score"`
	Threshold     float64 `json:"threshold"`
	Known         bool    `json:"known"`
	RunnerUp      string  `json:"runner_up,omitempty"`
	RunnerUpScore float64 `json:"runner_up_score,omitempty"`
	SpeakerID     int64   `json:"speaker_id,omitempty"`
}

// UnknownIdentity is what the pipeline uses when identification is impossible
// (no enrolled speakers, or the embedder is down).
func UnknownIdentity() Identity {
	return Identity{Name: Unknown, Role: UnknownRole, Known: false}
}

// Match compares an embedding against every enrolled speaker and returns the
// best candidate, whether or not it clears the threshold.
func Match(v Vector, records []Record, defaultThreshold float64) Identity {
	identity := UnknownIdentity()
	if len(v) == 0 || len(records) == 0 {
		return identity
	}

	best, second := -2.0, -2.0
	var bestRecord *Record
	var bestName, secondName string

	for i := range records {
		score := Cosine(v, records[i].Centroid)
		switch {
		case score > best:
			second, secondName = best, bestName
			best, bestRecord, bestName = score, &records[i], records[i].Name
		case score > second:
			second, secondName = score, records[i].Name
		}
	}
	if bestRecord == nil {
		return identity
	}

	threshold := bestRecord.EffectiveThreshold(defaultThreshold)
	identity = Identity{
		Name:      bestRecord.Name,
		Role:      bestRecord.Role,
		Score:     best,
		Threshold: threshold,
		Known:     best >= threshold,
		SpeakerID: bestRecord.ID,
	}
	if second > -2 {
		identity.RunnerUp = secondName
		identity.RunnerUpScore = second
	}
	if !identity.Known {
		identity.Name = Unknown
		identity.Role = UnknownRole
		identity.SpeakerID = 0
	}
	return identity
}

// Borderline reports whether the best score sits close enough to the threshold
// that a second opinion — the command audio — is worth the extra embedding call.
func Borderline(candidate Identity, margin float64) bool {
	if candidate.Threshold == 0 || margin <= 0 {
		return false
	}
	delta := candidate.Score - candidate.Threshold
	if delta < 0 {
		delta = -delta
	}
	return delta <= margin
}

// Apply resolves the unknown-speaker policy into the identity the intent router
// should use. "deny" is signalled by the returned bool.
func Apply(policy string, candidate Identity) (Identity, bool) {
	if candidate.Known {
		return candidate, true
	}
	switch policy {
	case "deny":
		return candidate, false
	case "allow":
		candidate.Role = RoleMember
		return candidate, true
	default: // restricted
		candidate.Role = UnknownRole
		return candidate, true
	}
}
