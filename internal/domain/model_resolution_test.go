package domain

import "testing"

func TestEffectiveModel(t *testing.T) {
	for _, tc := range []struct {
		name        string
		teamDefault string
		orgMaxTier  string
		wantModel   string
		wantSource  string
	}{
		{"cap clamps team", "opus", "sonnet", "sonnet", "org-cap"},
		{"team at cap", "sonnet", "sonnet", "sonnet", "team"},
		{"both empty fall back", "", "", "sonnet", "team"},
		{"no cap keeps team", "opus", "", "opus", "team"},
		{"team below cap", "haiku", "opus", "haiku", "team"},
		{"unknown team falls back, no cap", "gpt-9", "", "sonnet", "team"},
		{"unknown team falls back, capped above", "gpt-9", "opus", "sonnet", "team"},
		{"unknown cap ignored", "opus", "llama", "opus", "team"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model, source := EffectiveModel(tc.teamDefault, tc.orgMaxTier)
			if model != tc.wantModel || source != tc.wantSource {
				t.Errorf("EffectiveModel(%q, %q) = (%q, %q), want (%q, %q)",
					tc.teamDefault, tc.orgMaxTier, model, source, tc.wantModel, tc.wantSource)
			}
		})
	}
}

func TestParseTierOrdering(t *testing.T) {
	if !(TierHaiku < TierSonnet && TierSonnet < TierOpus) {
		t.Fatal("tier ordering must be Haiku < Sonnet < Opus for the cap to clamp correctly")
	}
	if TierUnknown != 0 {
		t.Errorf("TierUnknown should be the zero value, got %d", TierUnknown)
	}
}

func TestModelTierString(t *testing.T) {
	for _, tc := range []struct {
		tier ModelTier
		want string
	}{
		{TierHaiku, "haiku"},
		{TierSonnet, "sonnet"},
		{TierOpus, "opus"},
		{TierUnknown, ""},
	} {
		if got := tc.tier.String(); got != tc.want {
			t.Errorf("ModelTier(%d).String() = %q, want %q", tc.tier, got, tc.want)
		}
		// Round-trip: a named tier parses back to itself.
		if tc.want != "" && ParseTier(tc.want) != tc.tier {
			t.Errorf("ParseTier(%q) did not round-trip to tier %d", tc.want, tc.tier)
		}
	}
}
