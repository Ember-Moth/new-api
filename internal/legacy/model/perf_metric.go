package model

import (
	"sync"

	"github.com/QuantumNous/new-api/internal/module/usage/performance"
	"github.com/QuantumNous/new-api/internal/config/setting/perf_metrics_setting"
	"github.com/QuantumNous/new-api/internal/config/setting/ratio_setting"
)

var performanceStores sync.Map

// PerformanceStore shares the module collector with remaining relay callers.
func PerformanceStore() *performance.Store {
	if value, ok := performanceStores.Load(DB); ok {
		return value.(*performance.Store)
	}
	store := performance.New(performance.Dependencies{DB: DB, Settings: perf_metrics_setting.GetSetting, ActiveGroups: func() []string {
		ratios := ratio_setting.GetGroupRatioCopy()
		groups := make([]string, 0, len(ratios)+1)
		for group := range ratios {
			groups = append(groups, group)
		}
		return append(groups, "auto")
	}})
	actual, _ := performanceStores.LoadOrStore(DB, store)
	return actual.(*performance.Store)
}
