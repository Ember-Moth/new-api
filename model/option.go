package model

import (
	"context"
	"sync"

	"github.com/QuantumNous/new-api/internal/module/system"
	"github.com/QuantumNous/new-api/internal/module/system/entity"
)

type Option = entity.Option

var optionManagers sync.Map

// OptionManager bridges the remaining setup/payment/plugin writers to the module.
func OptionManager() *system.Options {
	if value, ok := optionManagers.Load(DB); ok {
		return value.(*system.Options)
	}
	manager := system.NewOptions(system.OptionDependencies{DB: DB, InvalidatePricing: InvalidatePricingCache})
	actual, _ := optionManagers.LoadOrStore(DB, manager)
	return actual.(*system.Options)
}
func ConfigureOptions(manager *system.Options) { optionManagers.Store(DB, manager) }
func UpdateOption(key, value string) error {
	return OptionManager().UpdateOption(context.Background(), key, value)
}
func UpdateOptionsBulk(values map[string]string) error {
	return OptionManager().UpdateOptionsBulk(context.Background(), values)
}
