package speaker

import "testing"

func records() []Record {
	return []Record{
		{ID: 1, Name: "tomer", Role: RoleOwner, Centroid: synthetic(1, 0)},
		{ID: 2, Name: "dana", Role: RoleMember, Centroid: synthetic(2, 0)},
		{ID: 3, Name: "kid", Role: RoleKid, Centroid: synthetic(3, 0)},
	}
}

func TestMatchIdentifiesTheRightVoice(t *testing.T) {
	// A noisy sample of voice 2 must still match dana.
	identity := Match(synthetic(2, 0.4), records(), 0.45)
	if identity.Name != "dana" {
		t.Fatalf("matched %q, want dana (score %.3f)", identity.Name, identity.Score)
	}
	if !identity.Known {
		t.Fatalf("dana was not recognised: score %.3f, threshold %.3f", identity.Score, identity.Threshold)
	}
	if identity.Role != RoleMember {
		t.Fatalf("role = %q, want member", identity.Role)
	}
	if identity.RunnerUp == "" || identity.RunnerUp == "dana" {
		t.Fatalf("runner-up = %q, want another speaker", identity.RunnerUp)
	}
	if identity.RunnerUpScore >= identity.Score {
		t.Fatalf("runner-up %.3f should score below the winner %.3f", identity.RunnerUpScore, identity.Score)
	}
}

func TestMatchReturnsUnknownBelowThreshold(t *testing.T) {
	identity := Match(synthetic(99, 0), records(), 0.45)
	if identity.Known {
		t.Fatalf("stranger matched %q with %.3f", identity.Name, identity.Score)
	}
	if identity.Name != Unknown {
		t.Fatalf("name = %q, want %q", identity.Name, Unknown)
	}
	if identity.Role != UnknownRole {
		t.Fatalf("role = %q, want %q", identity.Role, UnknownRole)
	}
	if identity.SpeakerID != 0 {
		t.Fatalf("speaker id = %d, want 0 for an unknown voice", identity.SpeakerID)
	}
}

func TestMatchWithNoEnrolledSpeakers(t *testing.T) {
	identity := Match(synthetic(1, 0), nil, 0.45)
	if identity.Known || identity.Name != Unknown {
		t.Fatalf("got %+v, want unknown", identity)
	}
}

func TestMatchWithEmptyVector(t *testing.T) {
	if identity := Match(nil, records(), 0.45); identity.Known {
		t.Fatal("an empty embedding must not match anyone")
	}
}

func TestPerSpeakerThresholdOverridesTheDefault(t *testing.T) {
	strict := 0.99
	list := records()
	list[1].Threshold = &strict

	identity := Match(synthetic(2, 0.4), list, 0.10)
	if identity.Known {
		t.Fatalf("dana's strict threshold was ignored (score %.3f)", identity.Score)
	}
	if identity.Threshold != strict {
		t.Fatalf("threshold = %v, want %v", identity.Threshold, strict)
	}
}

func TestEffectiveThreshold(t *testing.T) {
	override := 0.8
	tests := []struct {
		name   string
		record Record
		want   float64
	}{
		{"default", Record{}, 0.45},
		{"override", Record{Threshold: &override}, 0.8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.record.EffectiveThreshold(0.45); got != tc.want {
				t.Fatalf("EffectiveThreshold() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBorderline(t *testing.T) {
	tests := []struct {
		name      string
		score     float64
		threshold float64
		margin    float64
		want      bool
	}{
		{"just below", 0.42, 0.45, 0.05, true},
		{"just above", 0.48, 0.45, 0.05, true},
		{"clearly above", 0.80, 0.45, 0.05, false},
		{"clearly below", 0.10, 0.45, 0.05, false},
		{"margin disabled", 0.44, 0.45, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Borderline(Identity{Score: tc.score, Threshold: tc.threshold}, tc.margin)
			if got != tc.want {
				t.Fatalf("Borderline() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestApplyUnknownPolicy(t *testing.T) {
	unknown := UnknownIdentity()
	known := Identity{Name: "tomer", Role: RoleOwner, Known: true}

	tests := []struct {
		name        string
		policy      string
		identity    Identity
		wantRole    Role
		wantAllowed bool
	}{
		{"restricted keeps unknown at the bottom", "restricted", unknown, RoleAny, true},
		{"deny refuses to act", "deny", unknown, RoleAny, false},
		{"allow promotes to member", "allow", unknown, RoleMember, true},
		{"known speakers are untouched", "deny", known, RoleOwner, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, allowed := Apply(tc.policy, tc.identity)
			if allowed != tc.wantAllowed {
				t.Fatalf("allowed = %v, want %v", allowed, tc.wantAllowed)
			}
			if got.Role != tc.wantRole {
				t.Fatalf("role = %q, want %q", got.Role, tc.wantRole)
			}
		})
	}
}

func TestRoleOrdering(t *testing.T) {
	tests := []struct {
		speaker Role
		floor   Role
		want    bool
	}{
		{RoleOwner, RoleOwner, true},
		{RoleOwner, RoleMember, true},
		{RoleMember, RoleOwner, false},
		{RoleMember, RoleMember, true},
		{RoleKid, RoleMember, false},
		{RoleKid, RoleAny, true},
		{RoleAny, RoleAny, true},
		{RoleAny, RoleKid, false},
	}
	for _, tc := range tests {
		if got := tc.speaker.Allows(tc.floor); got != tc.want {
			t.Fatalf("%s.Allows(%s) = %v, want %v", tc.speaker, tc.floor, got, tc.want)
		}
	}
}

func TestParseRole(t *testing.T) {
	tests := map[string]Role{
		"owner": RoleOwner, "OWNER": RoleOwner, " member ": RoleMember,
		"kid": RoleKid, "any": RoleAny, "": RoleMember, "wizard": RoleMember,
	}
	for input, want := range tests {
		if got := ParseRole(input); got != want {
			t.Fatalf("ParseRole(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCheckSample(t *testing.T) {
	voice := []Vector{synthetic(5, 0.2), synthetic(5, 0.25)}

	t.Run("first sample is always accepted", func(t *testing.T) {
		if check := CheckSample(synthetic(5, 0), nil, 0.30); !check.Accepted {
			t.Fatalf("first sample rejected: %+v", check)
		}
	})

	t.Run("a matching sample is kept", func(t *testing.T) {
		check := CheckSample(synthetic(5, 0.2), voice, 0.30)
		if !check.Accepted {
			t.Fatalf("same voice rejected with similarity %.3f", check.Similarity)
		}
	})

	t.Run("a cough is rejected", func(t *testing.T) {
		check := CheckSample(synthetic(77, 0), voice, 0.30)
		if check.Accepted {
			t.Fatalf("unrelated audio accepted with similarity %.3f", check.Similarity)
		}
		if check.Reason == "" {
			t.Fatal("rejection needs a reason the user can act on")
		}
	})

	t.Run("an empty embedding is rejected", func(t *testing.T) {
		if check := CheckSample(nil, voice, 0.30); check.Accepted {
			t.Fatal("empty embedding accepted")
		}
	})
}
