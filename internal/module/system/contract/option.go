package contract

type Option struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
type OptionUpdateRequest struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}
