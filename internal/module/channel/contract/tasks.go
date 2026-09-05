package contract

// UpstreamUpdateTask distinguishes manual detection from scheduled detection.
// Manual detection stages changes for review and never auto-applies them.
type UpstreamUpdateTask struct {
	Manual bool `json:"manual,omitempty"`
}
