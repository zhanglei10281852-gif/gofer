package models

// Contact move phases. A move copies one CardDAV provider card into another
// connected address book and only deletes the source after the copy is
// durably confirmed. Every non-terminal phase can be resumed after a restart.
const (
	ContactMovePhasePrepared    = "prepared"
	ContactMovePhaseCopying     = "copying"
	ContactMovePhaseCopied      = "copied"
	ContactMovePhaseDeleting    = "deleting"
	ContactMovePhaseSourceConflict = "conflict_source_changed"
	ContactMovePhaseBlockedTarget  = "blocked_target"
	ContactMovePhaseBlockedAccount = "blocked_account"
	ContactMovePhaseRetry      = "failed_retry"
	ContactMovePhaseDone       = "done"
)

// Contact move outcomes once phase is done.
const (
	ContactMoveOutcomeMoved    = "moved"
	ContactMoveOutcomeKeptBoth = "kept_both"
	ContactMoveOutcomeCancelled = "cancelled"
)

// ContactMoveFailedEnd marks which endpoint blocked the move.
const (
	ContactMoveEndSource = "source"
	ContactMoveEndTarget = "target"
)

// ContactMoveTerminal reports whether a move phase no longer progresses.
func ContactMoveTerminal(phase string) bool {
	return phase == ContactMovePhaseDone
}

// ContactMove is the view-facing state of one card relocation.
type ContactMove struct {
	ID              string
	UserID          string
	ProfileID       string
	SourceCardID    string
	SourceProvider  string
	SourceAccountID string
	SourceBookID    string
	SourceRemoteID  string
	SourceEtag      string
	TargetAccountID string
	TargetBookID    string
	TargetRemoteID  string
	TargetEtag      string
	Phase           string
	Outcome         string
	LastError       string
	FailedEnd       string
	AttemptCount    int
	CreatedAt       string
	UpdatedAt       string
}

type ContactProfile struct {
	ID           string
	UserID       string
	DisplayName  string
	SortName     string
	PrimaryEmail string
	AvatarURL    string
	Notes        string
	Origin       string
	SyncEnabled  bool
	IsDeleted    bool
	// TrashedAt is when the profile went to the Trash, while it is there.
	// A deleted profile out of the Trash is deleted for good.
	TrashedAt       string
	CreatedAt       string
	UpdatedAt       string
	Cards           []ContactCard
	Fields          []ContactField
	SyncMemberships []ContactSyncMembership
	Moves           []ContactMove
	Insights        []ContactInsight
}

// ContactSyncMembership describes where Gofer should actively replicate a
// profile. It is deliberately separate from ContactCard, which describes a
// copy that already exists in a provider or in Gofer Local.
type ContactSyncMembership struct {
	ID            string
	UserID        string
	ProfileID     string
	AccountID     string
	AddressBookID string
	Enabled       bool
	Status        string
	LastError     string
}

type ContactInsight struct {
	Kind     string
	Severity string
	Title    string
	Message  string
	Field    string
	Source   string
	Count    int
}

type ContactSyncSetup struct {
	Contact        Contact
	Phase          string
	SearchMode     string
	SearchQuery    string
	Locations      []ContactSyncSetupLocation
	ConflictFields []ContactField
}

type ContactSyncSetupLocation struct {
	AccountID  string
	Label      string
	Provider   string
	Candidates []ContactSyncSetupCandidate
	Error      string
}

type ContactSyncSetupCandidate struct {
	Key          string
	RemoteID     string
	ContactID    string
	Name         string
	Email        string
	Phone        string
	Organization string
	MatchEmail   bool
	MatchPhone   bool
	MatchName    bool
}

type ContactCard struct {
	ID             string
	UserID         string
	ProfileID      string
	Kind           string
	Provider       string
	AccountID      string
	AddressBookID  string
	RemoteID       string
	Etag           string
	RawPayload     string
	RawPayloadType string
	SyncStatus     string
	LastError      string
	IsDeleted      bool
}

type ContactField struct {
	ID              string
	UserID          string
	ProfileID       string
	CardID          string
	Kind            string
	Label           string
	Value           string
	NormalizedValue string
	IsPrimary       bool
	Ordinal         int
	Source          string
	Confidence      float64
}
