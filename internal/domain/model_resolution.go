package domain

import "strings"

// ModelTier is an ordered rank over the Anthropic model tiers TF uses for
// scoring and delegation. The ordering (Haiku < Sonnet < Opus) is what lets
// an org cap clamp a team's default: a higher-tier team default collapses to
// the org's max. Only Anthropic tiers are in scope — non-Anthropic models
// (future Bedrock-Llama etc.) are a separate concern and parse as Unknown.
type ModelTier int

const (
	TierUnknown ModelTier = iota
	TierHaiku
	TierSonnet
	TierOpus
)

// ParseTier maps a model name to its tier. Unknown for empty or unrecognized
// names, so callers can apply their own fallback.
func ParseTier(s string) ModelTier {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "haiku":
		return TierHaiku
	case "sonnet":
		return TierSonnet
	case "opus":
		return TierOpus
	default:
		return TierUnknown
	}
}

// String maps a tier back to its model name. Empty string for Unknown.
func (t ModelTier) String() string {
	switch t {
	case TierHaiku:
		return "haiku"
	case TierSonnet:
		return "sonnet"
	case TierOpus:
		return "opus"
	default:
		return ""
	}
}

// EffectiveModel resolves the model a team should actually use, given the
// team's configured default and the org's max-tier cap. The team default
// wins unless it exceeds the cap, in which case the cap applies.
//
//   - teamDefault empty/unknown → falls back to "sonnet".
//   - orgMaxTier empty/unknown → no cap.
//
// source is "org-cap" when the cap clamped the team's choice, else "team" —
// it lets the UI explain "you're on Sonnet because your org caps at Sonnet".
func EffectiveModel(teamDefault, orgMaxTier string) (model, source string) {
	team := ParseTier(teamDefault)
	if team == TierUnknown {
		team = TierSonnet
	}
	cap := ParseTier(orgMaxTier)
	if cap == TierUnknown {
		return team.String(), "team"
	}
	if team > cap {
		return cap.String(), "org-cap"
	}
	return team.String(), "team"
}
