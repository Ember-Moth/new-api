package system

import (
	"context"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/system/contract"
	"github.com/QuantumNous/new-api/internal/module/system/entity"
)

func (s *Service) Instances(ctx context.Context) ([]contract.SystemInstanceResponse, error) {
	rows, err := s.ListSystemInstances(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]contract.SystemInstanceResponse, 0, len(rows))
	now := common.GetTimestamp()
	for _, row := range rows {
		result = append(result, instanceResponse(row, now))
	}
	return result, nil
}
func instanceResponse(instance *entity.SystemInstance, now int64) contract.SystemInstanceResponse {
	status := contract.SystemInstanceStatusOnline
	if now-instance.LastSeenAt > contract.SystemInstanceStaleAfterSeconds {
		status = contract.SystemInstanceStatusStale
	}
	return contract.SystemInstanceResponse{
		NodeName:          instance.NodeName,
		Status:            status,
		StaleAfterSeconds: contract.SystemInstanceStaleAfterSeconds,
		StartedAt:         instance.StartedAt,
		LastSeenAt:        instance.LastSeenAt,
		Info:              decodeSystemInstanceInfo(instance.Info),
	}
}

func decodeSystemInstanceInfo(data string) any {
	if data == "" {
		return nil
	}
	var value any
	if err := common.UnmarshalJsonStr(data, &value); err != nil {
		return data
	}
	return value
}
