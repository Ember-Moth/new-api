package options

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/system/entity"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
)

type Dependencies struct {
	DB                *gorm.DB
	InvalidatePricing func()
	ValidateTaskURL   func(string) error
	AliasPlugin       func(*jsplugin.RoutingGeneration, string) (string, bool)
}
type Manager struct {
	store *store
	mu    sync.Mutex
	deps  Dependencies
}

func New(deps Dependencies) *Manager { return &Manager{store: &store{db: deps.DB}, deps: deps} }

func (r *Manager) invalidatePricing() {
	if r.deps.InvalidatePricing != nil {
		r.deps.InvalidatePricing()
	}
}

func validateOptionValue(key string, value string) error {
	if key == operation_setting.ToolPriceOptionKey {
		return operation_setting.ValidateToolPricesJSON(value)
	}
	if key == operation_setting.ChannelTestConcurrencyOptionKey {
		return operation_setting.ValidateChannelTestConcurrency(value)
	}
	if key == "MaxTokenAutoGroups" {
		return setting.ValidateMaxTokenAutoGroups(value)
	}

	switch key {
	case "ModelRatio", "CompletionRatio", "ModelPrice", "CacheRatio", "CreateCacheRatio", "ImageRatio", "AudioRatio", "AudioCompletionRatio", "TopupGroupRatio":
		var values map[string]float64
		return common.UnmarshalJsonStr(value, &values)
	case "GroupRatio":
		return ratio_setting.CheckGroupRatio(value)
	case "GroupGroupRatio":
		var values map[string]map[string]float64
		return common.UnmarshalJsonStr(value, &values)
	case "UserUsableGroups":
		var values map[string]string
		return common.UnmarshalJsonStr(value, &values)
	case "AutoGroups", setting.TaskPluginDisabledFactoryKeysKey:
		var values []string
		return common.UnmarshalJsonStr(value, &values)
	case "ModelRequestRateLimitGroup":
		return setting.CheckModelRequestRateLimitGroup(value)
	case "AutomaticDisableStatusCodes", "AutomaticRetryStatusCodes":
		_, err := operation_setting.ParseHTTPStatusCodeRanges(value)
		return err
	}
	return nil
}

func (r *Manager) UpdateOption(ctx context.Context, key, value string) error {
	return r.UpdateOptionsBulk(ctx, map[string]string{key: value})
}

func (r *Manager) UpdateOptionsBulk(ctx context.Context, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if err := validateOptionValue(key, value); err != nil {
			return err
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := make([]entity.Option, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, entity.Option{Key: key, Value: values[key]})
	}
	if err := r.store.put(ctx, rows); err != nil {
		return err
	}
	for _, key := range keys {
		if err := r.ApplyRuntime(key, values[key]); err != nil {
			return err
		}
	}
	return nil
}

func (r *Manager) Reload(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows, err := r.store.all(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := validateOptionValue(row.Key, row.Value); err != nil {
			return err
		}
	}
	for _, row := range rows {
		if err := r.ApplyRuntime(row.Key, row.Value); err != nil {
			return err
		}
	}
	return nil
}

func (r *Manager) SyncOptions(ctx context.Context, frequency int) {
	if frequency <= 0 {
		return
	}
	ticker := time.NewTicker(time.Duration(frequency) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.Reload(ctx); err != nil {
				common.SysLog("failed to reload options: " + err.Error())
			}
		}
	}
}
