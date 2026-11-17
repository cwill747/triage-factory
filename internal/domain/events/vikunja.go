package events

import (
	"strconv"

	"github.com/sky-ai-eng/triage-factory/internal/domain"
)

type VikunjaTaskMetadata struct {
	TaskID    int64  `json:"task_id"`
	ProjectID int64  `json:"project_id"`
	Title     string `json:"title"`
	Done      bool   `json:"done"`
	Priority  int    `json:"priority"`
}

type VikunjaTaskPredicate struct {
	ProjectIDIn []string `json:"project_id_in,omitempty" doc:"Match Vikunja project IDs. Empty matches every project routed to this team."`
	Priority    *int     `json:"priority,omitempty" doc:"Match the task priority (0 through 5)."`
}

func (p VikunjaTaskPredicate) Matches(m VikunjaTaskMetadata) bool {
	return idIn(p.ProjectIDIn, strconv.FormatInt(m.ProjectID, 10)) && (p.Priority == nil || *p.Priority == m.Priority)
}

func init() {
	for _, et := range []string{domain.EventVikunjaTaskAvailable, domain.EventVikunjaTaskUpdated, domain.EventVikunjaTaskCompleted} {
		Register(NewSchema[VikunjaTaskMetadata, VikunjaTaskPredicate](et, OwnershipOwned))
	}
}
