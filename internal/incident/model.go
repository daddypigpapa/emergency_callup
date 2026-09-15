// Package incident manages the active incident, per-team missions, and
// per-member assignments (SPEC §4, §5, §5.3, §5.4). Live location fields on
// `assignment` (state, last_*, arrived_at, ...) are written by the tracker
// package during location processing; this package owns the administrative
// side: opening/closing incidents and (re)assigning missions.
package incident

import "errors"

// Assignment states (SPEC §5.1, matching the `assignment.state` CHECK).
const (
	StateNotified  = "NOTIFIED"
	StateLoggedIn  = "LOGGED_IN"
	StateLocDenied = "LOC_DENIED"
	StateMoving    = "MOVING"
	StateArrived   = "ARRIVED"
	StateLeft      = "LEFT"
)

// Non-assignment field-API status values (SPEC §5.1's extra table).
const (
	StatusIdle   = "IDLE"   // no active incident
	StatusNone   = "NONE"   // active incident exists, member not assigned
	StatusClosed = "CLOSED" // member's incident closed <=12h ago, no new active incident
)

const (
	IncidentActive = "active"
	IncidentClosed = "closed"
)

var (
	ErrNoActiveIncident  = errors.New("incident: no active incident")
	ErrIncidentActive    = errors.New("incident: an incident is already active")
	ErrIncidentClosed    = errors.New("incident: this incident is closed")
	ErrTeamTaskNotFound  = errors.New("incident: team has no task in this incident")
	ErrAssignmentMissing = errors.New("incident: member is not assigned in this incident")
	ErrNoTeamsRequested  = errors.New("incident: at least one team must be included")
	ErrTeamMissingValues = errors.New("incident: team is missing a mission or area and has no peacetime default")
)

// Incident mirrors the `incident` table.
type Incident struct {
	ID       int64
	TypeText string
	Message  string
	Status   string
	Version  int
	OpenedBy string
	OpenedAt int64
	ClosedBy string
	ClosedAt int64 // 0 if not closed
}

// TeamTask mirrors the `team_task` table.
type TeamTask struct {
	IncidentID  int64
	TeamNo      int64
	Mission     string
	AreaID      int64
	Version     int
	UpdatedBy   string
	UpdatedAt   int64
	RallyLat    float64 // snapshotted at open/update time, docs/SPEC_AREA_EDITOR.md §3.5
	RallyLng    float64
	Checkpoints []Checkpoint // nil if none
}

// Assignment mirrors the `assignment` table.
type Assignment struct {
	IncidentID      int64
	MemberID        int64
	TeamNo          int64
	MissionOverride *string
	AreaOverrideID  *int64
	EffMission      string
	EffAreaID       int64
	MissionVer      int
	AckVer          int
	State           string
	FirstLoginAt    *int64
	ArrivedAt       *int64
	LastLeftAt      *int64
	LeftCount       int
	LastLat5        *int64
	LastLng5        *int64
	LastAcc         *int64
	LastFixAt       *int64
	Suspect         bool
}

// AckPending reports SPEC §5.1's derived "임무변경 미확인" flag.
func (a *Assignment) AckPending() bool { return a.MissionVer > a.AckVer }

// TeamInput is one team's chosen mission/area for a new incident or a
// team-task update (SPEC §7.3 POST /a/incidents `teams[]`,
// PUT /a/incidents/current/teams/{no}).
type TeamInput struct {
	No      int64
	Mission string
	AreaID  int64
}

// MemberOverrideInput is one member's individual mission/area override,
// nil meaning "follow the team task" (SPEC §7.3 `members[]`).
type MemberOverrideInput struct {
	MemberID int64
	Mission  *string
	AreaID   *int64
}
