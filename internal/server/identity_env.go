package server

import (
	"github.com/sky-ai-eng/triage-factory/internal/auth"
	"github.com/sky-ai-eng/triage-factory/internal/domain"
)

func envProvides(group string) bool {
	return auth.EnvProvides(group)
}

func effectiveGitHubIdentityBaseURL(orgSet domain.OrgSettings, creds auth.Credentials) string {
	if orgSet.GitHubBaseURL != "" {
		return orgSet.GitHubBaseURL
	}
	return creds.GitHubURL
}

func effectiveJiraIdentityBaseURL(orgSet domain.OrgSettings, creds auth.Credentials) string {
	if orgSet.JiraBaseURL != "" {
		return orgSet.JiraBaseURL
	}
	return creds.JiraURL
}
