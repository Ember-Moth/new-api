package entity

// SystemInstance is ephemeral node metadata stored only in DragonflyDB.
type SystemInstance struct {
	NodeName   string `json:"node_name"`
	Info       string `json:"info"`
	StartedAt  int64  `json:"started_at"`
	LastSeenAt int64  `json:"last_seen_at"`
}
