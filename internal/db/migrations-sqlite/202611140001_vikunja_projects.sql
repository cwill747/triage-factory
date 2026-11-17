-- +goose Up
CREATE UNIQUE INDEX IF NOT EXISTS teams_id_org_unique ON teams (id, org_id);
CREATE TABLE vikunja_project_routes (
    org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    scope TEXT NOT NULL CHECK (scope <> ''),
    project_id INTEGER NOT NULL CHECK (project_id > 0),
    team_id TEXT NOT NULL,
    PRIMARY KEY (org_id, scope, project_id),
    FOREIGN KEY (team_id, org_id) REFERENCES teams(id, org_id) ON DELETE CASCADE
);
CREATE INDEX vikunja_project_routes_team_idx ON vikunja_project_routes (team_id);
ALTER TABLE entities ADD COLUMN vikunja_project_id INTEGER
    CHECK (vikunja_project_id IS NULL OR (source = 'vikunja' AND vikunja_project_id > 0));
UPDATE entities SET vikunja_project_id = json_extract(snapshot_json, '$.project_id')
    WHERE source = 'vikunja' AND json_valid(snapshot_json)
      AND json_extract(snapshot_json, '$.project_id') > 0;

-- +goose Down
SELECT 'down not supported';
