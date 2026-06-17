package poller

import (
	"context"
	"testing"

	"github.com/sky-ai-eng/triage-factory/internal/db"
	"github.com/sky-ai-eng/triage-factory/internal/domain"
	"github.com/sky-ai-eng/triage-factory/internal/integrations"
)

type identityHostOrgs struct {
	db.OrgsStore
	settings domain.OrgSettings
	err      error
}

func (o identityHostOrgs) GetSettingsSystem(context.Context, string) (domain.OrgSettings, error) {
	if o.err != nil {
		return domain.OrgSettings{}, o.err
	}
	return o.settings, nil
}

type identityHostSecrets struct {
	db.SecretStore
	values map[string]string
	err    error
}

func (s identityHostSecrets) GetSystem(_ context.Context, _ string, key string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.values[key], nil
}

type recordingReviewerUsers struct {
	db.UsersStore
	host  string
	login string
}

func (u *recordingReviewerUsers) UserIDsForGitHubLoginSystem(_ context.Context, host, login string) ([]string, error) {
	u.host = host
	u.login = login
	return []string{"user-1"}, nil
}

func TestGitHubIdentityHost_UsesSecretFallback(t *testing.T) {
	m := &Manager{
		orgs:    identityHostOrgs{settings: domain.OrgSettings{}},
		secrets: identityHostSecrets{values: map[string]string{integrations.KeyGitHubURL: "https://ghe.example.com"}},
	}
	got, err := m.githubIdentityHost(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("githubIdentityHost: %v", err)
	}
	if got != "https://ghe.example.com" {
		t.Fatalf("githubIdentityHost = %q, want SecretStore host", got)
	}
}

func TestReviewerResolver_UsesSecretFallbackHost(t *testing.T) {
	users := &recordingReviewerUsers{}
	m := &Manager{
		orgs:    identityHostOrgs{settings: domain.OrgSettings{}},
		secrets: identityHostSecrets{values: map[string]string{integrations.KeyGitHubURL: "https://ghe.example.com"}},
		users:   users,
	}
	resolver := m.reviewerResolver(context.Background(), "org-1", "", nil)
	if !resolver.KnownUser("octocat") {
		t.Fatal("KnownUser = false, want true from recording user store")
	}
	if users.host != "https://ghe.example.com" || users.login != "octocat" {
		t.Fatalf("reviewer lookup = (host=%q login=%q), want SecretStore GHES host and octocat", users.host, users.login)
	}
}
