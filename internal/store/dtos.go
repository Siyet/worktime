package store

type User struct {
	ID         string `json:"id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	PictureURL string `json:"picture_url"`
}

type Project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	Archived  bool   `json:"archived"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	DeletedAt *int64 `json:"deleted_at"`
	ServerSeq int64  `json:"server_seq,omitempty"`
}

type TimeEntry struct {
	ID          string  `json:"id"`
	ProjectID   *string `json:"project_id"`
	Description string  `json:"description"`
	StartedAt   int64   `json:"started_at"`
	StoppedAt   *int64  `json:"stopped_at"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
	DeletedAt   *int64  `json:"deleted_at"`
	ServerSeq   int64   `json:"server_seq,omitempty"`
}

type TimeOff struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	DateFrom  string `json:"date_from"`
	DateTo    string `json:"date_to"`
	Note      string `json:"note"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	DeletedAt *int64 `json:"deleted_at"`
	ServerSeq int64  `json:"server_seq,omitempty"`
}

type SyncChanges struct {
	Projects    []Project   `json:"projects,omitempty"`
	TimeEntries []TimeEntry `json:"time_entries,omitempty"`
	TimeOff     []TimeOff   `json:"time_off,omitempty"`
}

type SyncRequest struct {
	Since   int64       `json:"since"`
	Changes SyncChanges `json:"changes"`
}

type SyncResponse struct {
	Seq     int64       `json:"seq"`
	Changes SyncChanges `json:"changes"`
}

type ProjectReport struct {
	ProjectID *string `json:"project_id"`
	TotalMs   int64   `json:"total_ms"`
}

type TimeOffReport struct {
	Kind string `json:"kind"`
	Days int    `json:"days"`
}

type Report struct {
	Projects []ProjectReport `json:"projects"`
	TimeOff  []TimeOffReport `json:"time_off"`
}
