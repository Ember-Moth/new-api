package channel

import (
	"context"
	"errors"
	"strings"
)

// PricingSyncChannels exposes saved endpoints without loading channel secrets.
func (s *Service) PricingSyncChannels(ctx context.Context, ids []int) ([]*Channel, error) {
	return s.Runtime.PricingSyncChannels(ctx, ids)
}

func (s *Service) PricingSyncCredential(ctx context.Context, id int) (string, string, error) {
	row, err := s.Runtime.PricingSyncCredentialChannel(ctx, id)
	if err != nil {
		return "", "", err
	}
	key, _, apiErr := s.GetNextEnabledKey(row)
	if apiErr != nil {
		return "", "", apiErr
	}
	if strings.TrimSpace(key) == "" {
		return "", "", errors.New("no API key configured for this channel")
	}
	return row.GetBaseURL(), strings.TrimSpace(key), nil
}
