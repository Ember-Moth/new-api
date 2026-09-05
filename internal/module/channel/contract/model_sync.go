package contract

type OverwriteModelFields struct {
	ModelName string   `json:"model_name"`
	Fields    []string `json:"fields"`
}

type ModelSyncRequest struct {
	Overwrite []OverwriteModelFields `json:"overwrite"`
	Locale    string                 `json:"locale"`
}

// ModelSyncResponse preserves the sync/preview API's heterogeneous envelopes:
// counters and name lists for synchronization, field-value differences for
// previews, and source information on upstream failures.
type ModelSyncResponse map[string]any
