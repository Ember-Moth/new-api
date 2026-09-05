package contract

import "encoding/json"

// PrefillGroup is the reusable model/tag/endpoint group exposed by channel APIs.
type PrefillGroup struct {
	Id          int             `json:"id"`
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Items       json.RawMessage `json:"items"`
	Description string          `json:"description,omitempty"`
	CreatedTime int64           `json:"created_time"`
	UpdatedTime int64           `json:"updated_time"`
}
