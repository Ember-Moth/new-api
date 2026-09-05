package contract

type TwoFACodeRequest struct {
	Code string `json:"code" binding:"required"`
}

type TwoFASetup struct {
	Secret      string   `json:"secret"`
	QRCodeData  string   `json:"qr_code_data"`
	BackupCodes []string `json:"backup_codes"`
}

type TwoFAStatus struct {
	Enabled              bool `json:"enabled"`
	Locked               bool `json:"locked"`
	BackupCodesRemaining *int `json:"backup_codes_remaining,omitempty"`
}

type TwoFARecoveryRotation struct {
	*AuthBundle
	BackupCodes []string `json:"backup_codes"`
}
