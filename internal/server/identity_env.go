package server

import (
	"github.com/sky-ai-eng/triage-factory/internal/auth"
	"github.com/sky-ai-eng/triage-factory/internal/db"
	"github.com/sky-ai-eng/triage-factory/internal/domain"
	"github.com/sky-ai-eng/triage-factory/internal/runmode"
)

func envProvides(group string) bool {
	return auth.EnvProvides(group)
}

func effectiveGitHubIdentityBaseURL(orgSet domain.OrgSettings, creds auth.Credentials) string {
	return db.EffectiveGitHubBaseURL(orgSet.GitHubBaseURL, creds.GitHubURL)
}

func effectiveJiraIdentityBaseURL(orgSet domain.OrgSettings, creds auth.Credentials) string {
	return db.EffectiveJiraBaseURL(orgSet.JiraBaseURL, creds.JiraURL)
}

func localEnvGitHubPATForHost(host string, creds auth.Credentials) string {
	if runmode.Current() != runmode.ModeLocal || !envProvides("github") {
		return ""
	}
	envHost, ok := resolveGitHubHost(creds.GitHubURL)
	if !ok || envHost != host {
		return ""
	}
	return creds.GitHubPAT
}

func localEnvJiraPATForHost(host string, creds auth.Credentials) string {
	if runmode.Current() != runmode.ModeLocal || !envProvides("jira") {
		return ""
	}
	envHost, ok := resolveJiraHost(creds.JiraURL)
	if !ok || envHost != host {
		return ""
	}
	return creds.JiraPAT
}
