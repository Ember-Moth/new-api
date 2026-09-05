package contract

// ChannelTestTask configures a scheduled or manually requested health-test run.
type ChannelTestTask struct {
	Mode   string `json:"mode,omitempty"`
	Notify bool   `json:"notify,omitempty"`
}
