package paymentconfig

import (
	"context"
	"errors"
	"strconv"
	"time"
)

type ComplianceConfirmation struct {
	Confirmed    bool   `json:"confirmed"`
	TermsVersion string `json:"terms_version"`
	ConfirmedAt  int64  `json:"confirmed_at"`
	ConfirmedBy  int    `json:"confirmed_by"`
}

func (s *Service) ConfirmCompliance(ctx context.Context, userID int, ip string) (ComplianceConfirmation, error) {
	if userID <= 0 || s.deps.TermsVersion == "" {
		return ComplianceConfirmation{}, errors.New("合规确认参数错误")
	}
	now := time.Now().Unix()
	result := ComplianceConfirmation{Confirmed: true, TermsVersion: s.deps.TermsVersion, ConfirmedAt: now, ConfirmedBy: userID}
	err := s.deps.SaveOptions(ctx, map[string]string{
		"payment_setting.compliance_confirmed":     "true",
		"payment_setting.compliance_terms_version": result.TermsVersion,
		"payment_setting.compliance_confirmed_at":  strconv.FormatInt(now, 10),
		"payment_setting.compliance_confirmed_by":  strconv.Itoa(userID),
		"payment_setting.compliance_confirmed_ip":  ip,
	})
	return result, err
}
