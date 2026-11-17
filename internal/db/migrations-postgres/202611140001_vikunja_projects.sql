-- +goose Up
CREATE TABLE public.vikunja_project_routes (
    org_id uuid NOT NULL REFERENCES public.orgs(id) ON DELETE CASCADE,
    scope text NOT NULL CHECK (scope <> ''),
    project_id bigint NOT NULL CHECK (project_id > 0),
    team_id uuid NOT NULL,
    PRIMARY KEY (org_id, scope, project_id),
    FOREIGN KEY (team_id, org_id) REFERENCES public.teams(id, org_id) ON DELETE CASCADE
);
CREATE INDEX vikunja_project_routes_team_idx ON public.vikunja_project_routes (team_id);
ALTER TABLE public.vikunja_project_routes ENABLE ROW LEVEL SECURITY;
CREATE POLICY vikunja_project_routes_select ON public.vikunja_project_routes FOR SELECT TO tf_app
    USING (org_id = tf.current_org_id() AND tf.team_in_current_org(team_id) AND (tf.user_in_team(team_id) OR tf.user_is_org_admin(org_id)));
CREATE POLICY vikunja_project_routes_insert ON public.vikunja_project_routes FOR INSERT TO tf_app
    WITH CHECK (org_id = tf.current_org_id() AND tf.user_is_org_admin(org_id));
CREATE POLICY vikunja_project_routes_delete ON public.vikunja_project_routes FOR DELETE TO tf_app
    USING (org_id = tf.current_org_id() AND tf.user_is_org_admin(org_id));
GRANT SELECT, INSERT, DELETE ON public.vikunja_project_routes TO tf_app;

ALTER TABLE public.entities ADD COLUMN vikunja_project_id bigint
    CHECK (vikunja_project_id IS NULL OR (source = 'vikunja' AND vikunja_project_id > 0));
UPDATE public.entities SET vikunja_project_id = (snapshot_json->>'project_id')::bigint
    WHERE source = 'vikunja' AND (snapshot_json->>'project_id') ~ '^[1-9][0-9]*$';

-- +goose Down
SELECT 'down not supported';
