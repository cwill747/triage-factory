package vikunja

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sky-ai-eng/triage-factory/internal/db"
	"github.com/sky-ai-eng/triage-factory/internal/domain"
	"github.com/sky-ai-eng/triage-factory/internal/eventing/eventsource"
	"github.com/sky-ai-eng/triage-factory/internal/routing"
	"github.com/sky-ai-eng/triage-factory/internal/server"
	"github.com/sky-ai-eng/triage-factory/internal/server/httpx"
)

const connectionKey = "vikunja_connection"

type ProjectRoute = db.VikunjaProjectRoute

// The encrypted envelope binds the host and credential. Project routes are
// stored separately; Projects carries them in API responses and sealed bundles.
type Connection struct {
	BaseURL      string         `json:"base_url"`
	Token        string         `json:"token,omitempty"`
	Projects     []ProjectRoute `json:"projects"`
	PollInterval int            `json:"poll_interval_seconds"`
}

func decodeConnection(raw string) (Connection, error) {
	var c Connection
	if raw == "" {
		return c, nil
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return c, fmt.Errorf("invalid stored Vikunja connection")
	}
	if c.PollInterval < 30 {
		c.PollInterval = 60
	}
	return c, nil
}

func connectionState(raw string, err error) (eventsource.State, error) {
	if err != nil {
		return "", err
	}
	c, err := decodeConnection(raw)
	if err != nil {
		return "", err
	}
	if c.BaseURL == "" || c.Token == "" {
		return eventsource.StateUnconfigured, nil
	}
	return eventsource.StateAvailable, nil
}

func init() {
	eventsource.Register("vikunja", eventsource.Registration{
		HasHost: true, Polled: true,
		Probe: func(ctx context.Context, tx db.TxStores, org string) (eventsource.State, error) {
			return connectionState(tx.Secrets.Get(ctx, org, connectionKey))
		},
		SystemProbe: func(ctx context.Context, s db.Stores, org string) (eventsource.State, error) {
			return connectionState(s.Secrets.GetSystem(ctx, org, connectionKey))
		},
	})
	server.RegisterExtension("vikunja", install)
}

type integration struct {
	api server.ExtensionAPI
	// Serialize local-mode keychain writes with polls as well as DB-backed
	// changes, so disconnect cannot race a still-running local poll.
	mu sync.Mutex
}

func install(api server.ExtensionAPI) {
	h := &integration{api: api}
	routing.RegisterSource("vikunja", routing.SourceHooks{
		ResolveOwner: h.owner,
		TracksScope: func(ctx context.Context, evt domain.Event, team string) bool {
			owner, err := h.owner(ctx, evt.OrgID, evt, "")
			if err != nil {
				slog.ErrorContext(ctx, "Vikunja tracking lookup failed", "error", err)
				return true
			}
			return owner.Owner == team
		},
	})
	// This is an org-admin-only surface: both the connection and its project
	// routes are org-wide configuration, never writable by a plain member.
	api.API("GET /api/orgs/{org_id}/vikunja", h.get)
	api.APIMutating("PUT /api/orgs/{org_id}/vikunja", h.put)
	api.APIMutating("DELETE /api/orgs/{org_id}/vikunja", h.disconnect)
	api.OnReady(h.run)
}

func (h *integration) owner(ctx context.Context, org string, evt domain.Event, entityID string) (routing.OwnerResolution, error) {
	c, err := loadConnection(ctx, h.api.Stores(), org)
	if err != nil {
		return routing.OwnerResolution{}, err
	}
	var m struct {
		ProjectID int64 `json:"project_id"`
	}
	if err := json.Unmarshal([]byte(evt.MetadataJSON), &m); err != nil {
		return routing.Unowned(), nil
	}
	if entityID != "" {
		e, err := h.api.Stores().Entities.GetSystem(ctx, org, entityID)
		if err != nil {
			return routing.OwnerResolution{}, err
		}
		if e == nil || e.Scope != c.BaseURL {
			return routing.Unowned(), nil
		}
	}
	for _, p := range c.Projects {
		if p.ProjectID != m.ProjectID {
			continue
		}
		team, err := h.api.Stores().Teams.GetSystem(ctx, org, p.TeamID)
		if err != nil {
			return routing.OwnerResolution{}, err
		}
		if team != nil && team.DeletedAt == nil {
			return routing.OwnedBy(p.TeamID), nil
		}
	}
	return routing.Unowned(), nil
}

func (h *integration) get(w http.ResponseWriter, r *http.Request) {
	org, user, ok := h.api.Authz().RequireOrgAdmin(w, r)
	if !ok {
		return
	}
	var c Connection
	err := h.api.Tx().WithReadTx(r.Context(), org, user, func(tx db.TxStores) error {
		raw, err := tx.Secrets.Get(r.Context(), org, connectionKey)
		if err != nil {
			return err
		}
		c, err = decodeConnection(raw)
		if err != nil {
			return err
		}
		c.Projects, err = tx.VikunjaProjects.List(r.Context(), org, c.BaseURL)
		return err
	})
	if err != nil {
		httpx.InternalError(w, "vikunja", err)
		return
	}
	connected := c.Token != ""
	c.Token = ""
	if c.Projects == nil {
		c.Projects = []ProjectRoute{}
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		Connection
		Connected bool `json:"connected"`
	}{c, connected})
}

func (h *integration) put(w http.ResponseWriter, r *http.Request) {
	org, user, ok := h.api.Authz().RequireOrgAdmin(w, r)
	if !ok {
		return
	}
	var c Connection
	if !httpx.DecodeJSONStrict(w, r, &c) {
		return
	}
	base, err := CanonicalURL(c.BaseURL)
	if err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	c.BaseURL = base
	c.Token = strings.TrimSpace(c.Token)
	if c.PollInterval == 0 {
		c.PollInterval = 60
	}
	if c.PollInterval < 30 || c.PollInterval > 86400 || len(c.Projects) > 100 {
		httpx.BadRequest(w, "Use a poll interval from 30 to 86400 seconds and at most 100 projects.")
		return
	}
	seen := map[int64]bool{}
	for _, p := range c.Projects {
		if p.ProjectID <= 0 || seen[p.ProjectID] || p.TeamID == "" {
			httpx.BadRequest(w, "Each project needs a unique positive ID and a team.")
			return
		}
		seen[p.ProjectID] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	err = h.api.Tx().WithTx(r.Context(), org, user, func(tx db.TxStores) error {
		raw, err := tx.Secrets.Get(r.Context(), org, connectionKey)
		if err != nil {
			return err
		}
		old, err := decodeConnection(raw)
		if err != nil {
			return err
		}
		if old.BaseURL != "" && old.BaseURL != c.BaseURL {
			return httpx.NewFault(409, "conflict", "Disconnect Vikunja before changing its installation URL.")
		}
		if c.Token == "" {
			c.Token = old.Token
		}
		client, err := NewClient(c.BaseURL, c.Token)
		if err != nil {
			return httpx.NewFault(400, "invalid", err.Error())
		}
		// Validate both sides of each route before committing any configuration.
		for _, p := range c.Projects {
			team, err := tx.Teams.Get(r.Context(), org, p.TeamID)
			if err != nil {
				return err
			}
			if team == nil || team.DeletedAt != nil {
				return httpx.NewFault(400, "invalid", "Choose an active team in this organization.")
			}
			if _, err := client.Project(r.Context(), p.ProjectID); err != nil {
				return httpx.NewFault(502, "upstream_unavailable", err.Error())
			}
		}
		// Reading tasks verifies the token even when the tracked set is empty.
		if len(c.Projects) == 0 {
			return httpx.NewFault(400, "invalid", "Configure at least one project route.")
		}
		if _, err := client.Tasks(r.Context(), c.Projects[0].ProjectID); err != nil {
			return httpx.NewFault(502, "upstream_unavailable", err.Error())
		}
		if err := tx.VikunjaProjects.Replace(r.Context(), org, c.BaseURL, c.Projects); err != nil {
			return err
		}
		credential := c
		credential.Projects = nil
		encoded, err := json.Marshal(credential)
		if err != nil {
			return err
		}
		return tx.Secrets.Put(r.Context(), org, connectionKey, string(encoded), "Vikunja installation and API token")
	})
	if err != nil {
		if !httpx.WriteFault(w, err) {
			httpx.InternalError(w, "vikunja", err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *integration) disconnect(w http.ResponseWriter, r *http.Request) {
	org, user, ok := h.api.Authz().RequireOrgAdmin(w, r)
	if !ok {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	err := h.api.Tx().WithTx(r.Context(), org, user, func(tx db.TxStores) error {
		if err := tx.VikunjaProjects.Replace(r.Context(), org, "", nil); err != nil {
			return err
		}
		_, err := tx.Secrets.Delete(r.Context(), org, connectionKey)
		return err
	})
	if err != nil {
		httpx.InternalError(w, "vikunja", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *integration) run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	next := map[string]time.Time{}
	for {
		h.pollOrgs(ctx, next)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *integration) pollOrgs(ctx context.Context, next map[string]time.Time) {
	orgs, err := h.api.Stores().Orgs.ListActiveSystem(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "Vikunja org scan failed", "error", err)
		return
	}
	for _, org := range orgs {
		if ctx.Err() != nil {
			return
		}
		if time.Now().Before(next[org]) {
			continue
		}
		h.mu.Lock()
		interval, err := h.pollOrg(ctx, org)
		h.mu.Unlock()
		if interval < 30 {
			interval = 60
		}
		next[org] = time.Now().Add(time.Duration(interval) * time.Second)
		if err != nil {
			slog.ErrorContext(ctx, "Vikunja poll failed", "org", org, "error", err)
		}
	}
}

func (h *integration) pollOrg(ctx context.Context, org string) (int, error) {
	s := h.api.Stores()
	off, err := eventsource.Disabled(ctx, s.OrgEventSources, org, "vikunja")
	if err != nil || off {
		return 60, err
	}
	c, err := loadConnection(ctx, s, org)
	if err != nil || c.Token == "" {
		return 60, err
	}
	client, err := NewClient(c.BaseURL, c.Token)
	if err != nil {
		return c.PollInterval, err
	}
	var failures []error
	seen := map[int64]bool{}
	for _, route := range c.Projects {
		if err := ctx.Err(); err != nil {
			return c.PollInterval, errors.Join(append(failures, err)...)
		}
		tasks, err := client.Tasks(ctx, route.ProjectID)
		if err != nil {
			failures = append(failures, fmt.Errorf("project %d: %w", route.ProjectID, err))
			continue
		}
		for _, task := range tasks {
			if err := ctx.Err(); err != nil {
				return c.PollInterval, errors.Join(append(failures, err)...)
			}
			seen[task.ID] = true
			if err := h.observe(ctx, org, c, task); err != nil {
				failures = append(failures, fmt.Errorf("task %d: %w", task.ID, err))
			}
		}
	}
	// Removing a project is forward-only. Refresh already-known active tasks
	// by ID so their existing TF tasks still close when marked done. Absence
	// from a list is never treated as deletion or completion.
	active, err := s.Entities.ListActiveSystem(ctx, org, "vikunja")
	if err != nil {
		return c.PollInterval, errors.Join(append(failures, err)...)
	}
	for _, e := range active {
		if err := ctx.Err(); err != nil {
			return c.PollInterval, errors.Join(append(failures, err)...)
		}
		if e.Scope != c.BaseURL {
			continue
		}
		id, err := strconv.ParseInt(e.ExternalID, 10, 64)
		if err != nil {
			failures = append(failures, fmt.Errorf("entity %s: %w", e.ID, err))
			continue
		}
		if seen[id] {
			continue
		}
		task, err := client.Task(ctx, id)
		if err != nil {
			failures = append(failures, fmt.Errorf("task %d: %w", id, err))
			continue
		}
		if err := h.observe(ctx, org, c, task); err != nil {
			failures = append(failures, fmt.Errorf("task %d: %w", id, err))
		}
	}
	return c.PollInterval, errors.Join(failures...)
}
