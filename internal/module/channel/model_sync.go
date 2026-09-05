package channel

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/module/channel/contract"
	"github.com/QuantumNous/new-api/internal/module/channel/internal/upstream"
)

func (s *Service) ensureVendorID(ctx context.Context, vendorName string, vendorByName map[string]upstream.Vendor, vendorIDCache map[string]int, createdVendors *int) int {
	if vendorName == "" {
		return 0
	}
	if id, ok := vendorIDCache[vendorName]; ok {
		return id
	}
	if existing, err := s.VendorByName(ctx, vendorName); err == nil {
		vendorIDCache[vendorName] = existing.Id
		return existing.Id
	}
	uv := vendorByName[vendorName]
	v := &contract.Vendor{
		Name:        vendorName,
		Description: uv.Description,
		Icon:        coalesce(uv.Icon, ""),
		Status:      chooseStatus(uv.Status, 1),
	}
	if err := s.CreateVendor(ctx, v); err == nil {
		*createdVendors++
		vendorIDCache[vendorName] = v.Id
		return v.Id
	}
	vendorIDCache[vendorName] = 0
	return 0
}

// SyncUpstreamModels 同步上游模型与供应商：
// - 默认仅创建「未配置模型」
// - 可通过 overwrite 选择性覆盖更新本地已有模型的字段（前提：sync_official <> 0）
func (s *Service) SyncUpstreamModels(ctx context.Context, req contract.ModelSyncRequest) contract.ModelSyncResponse {
	// 1) 获取未配置模型列表
	missing, err := s.MissingModels(ctx)
	if err != nil {
		common.SysError("failed to get missing models: " + err.Error())
		return contract.ModelSyncResponse{"success": false, "message": "获取模型列表失败，请稍后重试"}
	}

	// 若既无缺失模型需要创建，也未指定覆盖更新字段，则无需请求上游数据，直接返回
	if len(missing) == 0 && len(req.Overwrite) == 0 {
		modelsURL, vendorsURL := upstream.URLs(req.Locale)
		return contract.ModelSyncResponse{
			"success": true,
			"data": map[string]any{
				"created_models":  0,
				"created_vendors": 0,
				"updated_models":  0,
				"skipped_models":  []string{},
				"created_list":    []string{},
				"updated_list":    []string{},
				"source": map[string]any{
					"locale":      req.Locale,
					"models_url":  modelsURL,
					"vendors_url": vendorsURL,
				},
			},
		}
	}

	// 2) 拉取上游 vendors 与 models
	timeoutSec := common.GetEnvOrDefault("SYNC_HTTP_TIMEOUT_SECONDS", 15)
	fetchCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	modelsURL, vendorsURL := upstream.URLs(req.Locale)
	var vendorsEnv upstream.Envelope[upstream.Vendor]
	var modelsEnv upstream.Envelope[upstream.Model]
	var fetchErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		// vendor 失败不拦截
		_ = upstream.FetchJSON(s.upstream, fetchCtx, vendorsURL, &vendorsEnv)
	}()
	go func() {
		defer wg.Done()
		if err := upstream.FetchJSON(s.upstream, fetchCtx, modelsURL, &modelsEnv); err != nil {
			fetchErr = err
		}
	}()
	wg.Wait()
	if fetchErr != nil {
		return contract.ModelSyncResponse{"success": false, "message": "获取上游模型失败: " + fetchErr.Error(), "locale": req.Locale, "source_urls": map[string]any{"models_url": modelsURL, "vendors_url": vendorsURL}}
	}

	// 建立映射
	vendorByName := make(map[string]upstream.Vendor)
	for _, v := range vendorsEnv.Data {
		if v.Name != "" {
			vendorByName[v.Name] = v
		}
	}
	modelByName := make(map[string]upstream.Model)
	for _, m := range modelsEnv.Data {
		if m.ModelName != "" {
			modelByName[m.ModelName] = m
		}
	}

	// 3) 执行同步：仅创建缺失模型；若上游缺失该模型则跳过
	createdModels := 0
	createdVendors := 0
	updatedModels := 0
	skipped := make([]string, 0)
	createdList := make([]string, 0)
	updatedList := make([]string, 0)

	// 本地缓存：vendorName -> id
	vendorIDCache := make(map[string]int)

	for _, name := range missing {
		up, ok := modelByName[name]
		if !ok {
			skipped = append(skipped, name)
			continue
		}

		// 若本地已存在且设置为不同步，则跳过（极端情况：缺失列表与本地状态不同步时）
		if existing, err := s.catalog.ModelByName(ctx, name); err == nil {
			if existing.SyncOfficial == 0 {
				skipped = append(skipped, name)
				continue
			}
		}

		// 确保 vendor 存在
		vendorID := s.ensureVendorID(ctx, up.VendorName, vendorByName, vendorIDCache, &createdVendors)

		// 创建模型
		mi := &contract.Model{
			ModelName:   name,
			Description: up.Description,
			Icon:        up.Icon,
			Tags:        up.Tags,
			VendorID:    vendorID,
			Status:      chooseStatus(up.Status, 1),
			NameRule:    up.NameRule,
		}
		now := time.Now().Unix()
		mi.CreatedTime, mi.UpdatedTime = now, now
		if err := s.catalog.SaveModel(ctx, mi, true); err == nil {
			createdModels++
			createdList = append(createdList, name)
		} else {
			skipped = append(skipped, name)
		}
	}

	// 4) 处理可选覆盖（更新本地已有模型的差异字段）
	if len(req.Overwrite) > 0 {
		// vendorIDCache 已用于创建阶段，可复用
		for _, ow := range req.Overwrite {
			up, ok := modelByName[ow.ModelName]
			if !ok {
				continue
			}
			local, err := s.catalog.ModelByName(ctx, ow.ModelName)
			if err != nil {
				continue
			}

			// 跳过被禁用官方同步的模型
			if local.SyncOfficial == 0 {
				continue
			}

			// 映射 vendor
			newVendorID := s.ensureVendorID(ctx, up.VendorName, vendorByName, vendorIDCache, &createdVendors)

			// 应用字段覆盖（事务）
			needUpdate := false
			if containsField(ow.Fields, "description") {
				local.Description = up.Description
				needUpdate = true
			}
			if containsField(ow.Fields, "icon") {
				local.Icon = up.Icon
				needUpdate = true
			}
			if containsField(ow.Fields, "tags") {
				local.Tags = up.Tags
				needUpdate = true
			}
			if containsField(ow.Fields, "vendor") {
				local.VendorID = newVendorID
				needUpdate = true
			}
			if containsField(ow.Fields, "name_rule") {
				local.NameRule = up.NameRule
				needUpdate = true
			}
			if containsField(ow.Fields, "status") {
				local.Status = chooseStatus(up.Status, local.Status)
				needUpdate = true
			}
			if !needUpdate {
				continue
			}
			if err := s.catalog.SaveSyncedModel(ctx, local); err != nil {
				continue
			}
			updatedModels++
			updatedList = append(updatedList, ow.ModelName)
		}
	}

	if (createdModels > 0 || updatedModels > 0) && s.pricing != nil {
		s.pricing.Refresh()
	}
	return contract.ModelSyncResponse{
		"success": true,
		"data": map[string]any{
			"created_models":  createdModels,
			"created_vendors": createdVendors,
			"updated_models":  updatedModels,
			"skipped_models":  skipped,
			"created_list":    createdList,
			"updated_list":    updatedList,
			"source": map[string]any{
				"locale":      req.Locale,
				"models_url":  modelsURL,
				"vendors_url": vendorsURL,
			},
		},
	}
}

func containsField(fields []string, key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, f := range fields {
		if strings.ToLower(strings.TrimSpace(f)) == key {
			return true
		}
	}
	return false
}

func coalesce(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func chooseStatus(primary, fallback int) int {
	if primary == 0 && fallback != 0 {
		return fallback
	}
	if primary != 0 {
		return primary
	}
	return 1
}

// SyncUpstreamPreview 预览上游与本地的差异（仅用于弹窗选择）
func (s *Service) SyncUpstreamPreview(ctx context.Context, locale string) contract.ModelSyncResponse {
	// 1) 拉取上游数据
	timeoutSec := common.GetEnvOrDefault("SYNC_HTTP_TIMEOUT_SECONDS", 15)
	fetchCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	modelsURL, vendorsURL := upstream.URLs(locale)

	var vendorsEnv upstream.Envelope[upstream.Vendor]
	var modelsEnv upstream.Envelope[upstream.Model]
	var fetchErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = upstream.FetchJSON(s.upstream, fetchCtx, vendorsURL, &vendorsEnv)
	}()
	go func() {
		defer wg.Done()
		if err := upstream.FetchJSON(s.upstream, fetchCtx, modelsURL, &modelsEnv); err != nil {
			fetchErr = err
		}
	}()
	wg.Wait()
	if fetchErr != nil {
		return contract.ModelSyncResponse{"success": false, "message": "获取上游模型失败: " + fetchErr.Error(), "locale": locale, "source_urls": map[string]any{"models_url": modelsURL, "vendors_url": vendorsURL}}
	}

	vendorByName := make(map[string]upstream.Vendor)
	for _, v := range vendorsEnv.Data {
		if v.Name != "" {
			vendorByName[v.Name] = v
		}
	}
	modelByName := make(map[string]upstream.Model)
	upstreamNames := make([]string, 0, len(modelsEnv.Data))
	for _, m := range modelsEnv.Data {
		if m.ModelName != "" {
			modelByName[m.ModelName] = m
			upstreamNames = append(upstreamNames, m.ModelName)
		}
	}

	// 2) 本地已有模型
	var locals []*contract.Model
	if len(upstreamNames) > 0 {
		locals, _ = s.catalog.OfficialModelsByNames(ctx, upstreamNames)
	}

	// 本地 vendor 名称映射
	vendorIdSet := make(map[int]struct{})
	for _, m := range locals {
		if m.VendorID != 0 {
			vendorIdSet[m.VendorID] = struct{}{}
		}
	}
	vendorIDs := make([]int, 0, len(vendorIdSet))
	for id := range vendorIdSet {
		vendorIDs = append(vendorIDs, id)
	}
	idToVendorName := make(map[int]string)
	if len(vendorIDs) > 0 {
		dbVendors, _ := s.VendorsByIDs(ctx, vendorIDs)
		for _, v := range dbVendors {
			idToVendorName[v.Id] = v.Name
		}
	}

	// 3) 缺失且上游存在的模型
	missingList, _ := s.MissingModels(ctx)
	var missing []string
	for _, name := range missingList {
		if _, ok := modelByName[name]; ok {
			missing = append(missing, name)
		}
	}

	// 4) 计算冲突字段
	type conflictField struct {
		Field    string      `json:"field"`
		Local    interface{} `json:"local"`
		Upstream interface{} `json:"upstream"`
	}
	type conflictItem struct {
		ModelName string          `json:"model_name"`
		Fields    []conflictField `json:"fields"`
	}

	var conflicts []conflictItem
	for _, local := range locals {
		up, ok := modelByName[local.ModelName]
		if !ok {
			continue
		}
		fields := make([]conflictField, 0, 6)
		if strings.TrimSpace(local.Description) != strings.TrimSpace(up.Description) {
			fields = append(fields, conflictField{Field: "description", Local: local.Description, Upstream: up.Description})
		}
		if strings.TrimSpace(local.Icon) != strings.TrimSpace(up.Icon) {
			fields = append(fields, conflictField{Field: "icon", Local: local.Icon, Upstream: up.Icon})
		}
		if strings.TrimSpace(local.Tags) != strings.TrimSpace(up.Tags) {
			fields = append(fields, conflictField{Field: "tags", Local: local.Tags, Upstream: up.Tags})
		}
		// vendor 对比使用名称
		localVendor := idToVendorName[local.VendorID]
		if strings.TrimSpace(localVendor) != strings.TrimSpace(up.VendorName) {
			fields = append(fields, conflictField{Field: "vendor", Local: localVendor, Upstream: up.VendorName})
		}
		if local.NameRule != up.NameRule {
			fields = append(fields, conflictField{Field: "name_rule", Local: local.NameRule, Upstream: up.NameRule})
		}
		if local.Status != chooseStatus(up.Status, local.Status) {
			fields = append(fields, conflictField{Field: "status", Local: local.Status, Upstream: up.Status})
		}
		if len(fields) > 0 {
			conflicts = append(conflicts, conflictItem{ModelName: local.ModelName, Fields: fields})
		}
	}

	return contract.ModelSyncResponse{
		"success": true,
		"data": map[string]any{
			"missing":   missing,
			"conflicts": conflicts,
			"source": map[string]any{
				"locale":      locale,
				"models_url":  modelsURL,
				"vendors_url": vendorsURL,
			},
		},
	}
}
