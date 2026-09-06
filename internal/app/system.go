package app

import (
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/system/contract"
)

func systemInstanceResources() contract.SystemInstanceResources {
	status := common.GetSystemStatus()
	disk := common.GetDiskSpaceInfo()
	return contract.SystemInstanceResources{
		CPU:     contract.SystemInstanceResourceUsage{UsagePercent: status.CPUUsage},
		Memory:  contract.SystemInstanceResourceUsage{UsagePercent: status.MemoryUsage},
		Storage: contract.SystemInstanceStorageMetrics{TotalBytes: disk.Total, UsedBytes: disk.Used, FreeBytes: disk.Free, UsedPercent: disk.UsedPercent},
	}
}
