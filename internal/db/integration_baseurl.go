package db

// EffectiveGitHubBaseURL returns the GitHub URL that should be used for
// identity and routing lookups: the mirrored org setting wins, with the
// legacy SecretStore value as the local/env fallback.
func EffectiveGitHubBaseURL(orgBase, secretBase string) string {
	if orgBase != "" {
		return orgBase
	}
	return secretBase
}

// EffectiveJiraBaseURL returns the Jira URL that should be used for identity
// and per-user credential lookups: the mirrored org setting wins, with the
// legacy SecretStore value as the local/env fallback.
func EffectiveJiraBaseURL(orgBase, secretBase string) string {
	if orgBase != "" {
		return orgBase
	}
	return secretBase
}
