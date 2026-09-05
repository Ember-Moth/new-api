package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/infra/database/query"
	"github.com/QuantumNous/new-api/internal/infra/database/value"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/shopspring/decimal"
)

// TokenValidationError leaves localization to the inbound adapter.
type TokenValidationError struct {
	Code    string
	Details map[string]any
}

func (e *TokenValidationError) Error() string { return e.Code }

func (s *Service) ListTokens(ctx context.Context, userID, offset, limit int) ([]*contract.TokenResponse, int64, error) {
	rows, total, err := s.tokens.List(ctx, userID, offset, limit, "", "")
	if err != nil {
		return nil, 0, err
	}
	return maskedTokens(rows), total, nil
}

func (s *Service) SearchTokens(ctx context.Context, userID int, keyword, key string, offset, limit int) ([]*contract.TokenResponse, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	key = strings.TrimPrefix(key, "sk-")
	if strings.Contains(keyword, "%") || strings.Contains(key, "%") {
		count, err := s.tokens.Count(ctx, userID)
		if err != nil {
			common.SysLog("failed to count user tokens: " + err.Error())
			return nil, 0, errors.New("获取令牌数量失败")
		}
		if int(count) > s.tokenPolicy.MaxTokens() {
			return nil, 0, errors.New("令牌数量超过上限，仅允许精确搜索，请勿使用 % 通配符")
		}
	}
	namePattern, err := query.SanitizeLikePattern(keyword)
	if err != nil {
		return nil, 0, err
	}
	keyPattern, err := query.SanitizeLikePattern(key)
	if err != nil {
		return nil, 0, err
	}
	rows, total, err := s.tokens.List(ctx, userID, offset, limit, namePattern, keyPattern)
	if err != nil {
		common.SysError("failed to search tokens: " + err.Error())
		return nil, 0, errors.New("搜索令牌失败")
	}
	return maskedTokens(rows), total, nil
}

func (s *Service) TokenRecord(ctx context.Context, id, userID int) (*entity.Token, error) {
	if id == 0 || userID == 0 {
		return nil, errors.New("id 或 userId 为空！")
	}
	return s.tokens.Get(ctx, id, userID)
}

func (s *Service) GetToken(ctx context.Context, id, userID int) (*contract.TokenResponse, error) {
	token, err := s.TokenRecord(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	return maskedToken(token), nil
}

func (s *Service) TokenKey(ctx context.Context, id, userID int) (string, error) {
	token, err := s.TokenRecord(ctx, id, userID)
	if err != nil {
		return "", err
	}
	return token.GetFullKey(), nil
}

func (s *Service) TokenKeys(ctx context.Context, ids []int, userID int) (map[int]string, error) {
	if len(ids) == 0 {
		return nil, &TokenValidationError{Code: "invalid_params"}
	}
	if len(ids) > 100 {
		return nil, &TokenValidationError{Code: "batch_too_many", Details: map[string]any{"Max": 100}}
	}
	rows, err := s.tokens.Keys(ctx, ids, userID)
	if err != nil {
		return nil, err
	}
	result := make(map[int]string, len(rows))
	for _, row := range rows {
		result[row.Id] = row.GetFullKey()
	}
	return result, nil
}

func (s *Service) TokenAutoGroupOptions(ctx context.Context, actor contract.TokenActor) (*contract.TokenAutoGroupOptions, error) {
	group, err := s.tokenActorGroup(ctx, actor)
	if err != nil {
		return nil, err
	}
	return &contract.TokenAutoGroupOptions{Groups: s.tokenPolicy.AutoGroups(group), MaxCount: s.tokenPolicy.MaxAutoGroups()}, nil
}

// ProvisionToken creates a trusted registration token. Public management requests
// use CreateToken, which generates the key and enforces the configured limits.
func (s *Service) ProvisionToken(ctx context.Context, token *entity.Token) error {
	return s.tokens.Create(ctx, token)
}

func (s *Service) CreateToken(ctx context.Context, actor contract.TokenActor, request contract.TokenRequest) error {
	if err := validateTokenSettings(request.TokenSettings); err != nil {
		return err
	}
	count, err := s.tokens.Count(ctx, actor.ID)
	if err != nil {
		return err
	}
	maxTokens := s.tokenPolicy.MaxTokens()
	if int(count) >= maxTokens {
		return fmt.Errorf("已达到最大令牌数量限制 (%d)", maxTokens)
	}
	token := &entity.Token{UserId: actor.ID, CreatedTime: common.GetTimestamp(), AccessedTime: common.GetTimestamp()}
	applyTokenSettings(token, request.TokenSettings)
	if token.Group == "auto" {
		if err := s.setTokenAutoGroups(ctx, actor, token, request.AutoGroups.Groups); err != nil {
			return err
		}
	} else {
		token.CrossGroupRetry = false
		token.AutoGroups = nil
	}
	key, err := common.GenerateKey()
	if err != nil {
		common.SysLog("failed to generate token key: " + err.Error())
		return &TokenValidationError{Code: "generate_failed"}
	}
	token.Key = key
	return s.tokens.Create(ctx, token)
}

func (s *Service) UpdateToken(ctx context.Context, actor contract.TokenActor, request contract.TokenRequest, statusOnly bool) (*contract.TokenResponse, error) {
	if err := validateTokenSettings(request.TokenSettings); err != nil {
		return nil, err
	}
	token, err := s.TokenRecord(ctx, request.Id, actor.ID)
	if err != nil {
		return nil, err
	}
	if request.Status == common.TokenStatusEnabled {
		if token.Status == common.TokenStatusExpired && token.ExpiredTime <= common.GetTimestamp() && token.ExpiredTime != -1 {
			return nil, &TokenValidationError{Code: "expired_cannot_enable"}
		}
		if token.Status == common.TokenStatusExhausted && token.RemainQuota <= 0 && !token.UnlimitedQuota {
			return nil, &TokenValidationError{Code: "exhausted_cannot_enable"}
		}
	}
	updateAutoGroups := false
	if statusOnly {
		token.Status = request.Status
	} else {
		applyTokenSettings(token, request.TokenSettings)
		if token.Group != "auto" {
			token.CrossGroupRetry = false
			token.AutoGroups = nil
			updateAutoGroups = true
		} else if request.AutoGroups.Set {
			if err := s.setTokenAutoGroups(ctx, actor, token, request.AutoGroups.Groups); err != nil {
				return nil, err
			}
			updateAutoGroups = true
		}
	}
	if err := s.tokens.Update(ctx, token, statusOnly, updateAutoGroups); err != nil {
		return nil, err
	}
	return maskedToken(token), nil
}

func (s *Service) DeleteToken(ctx context.Context, id, userID int) error {
	token, err := s.TokenRecord(ctx, id, userID)
	if err != nil {
		return err
	}
	return s.tokens.Delete(ctx, token)
}

func (s *Service) DeleteTokens(ctx context.Context, ids []int, userID int) (int, error) {
	if len(ids) == 0 {
		return 0, &TokenValidationError{Code: "invalid_params"}
	}
	return s.tokens.DeleteBatch(ctx, ids, userID)
}

func validateTokenSettings(token contract.TokenSettings) error {
	if len(token.Name) > 50 {
		return &TokenValidationError{Code: "name_too_long"}
	}
	if !token.UnlimitedQuota {
		if token.RemainQuota < 0 {
			return &TokenValidationError{Code: "quota_negative"}
		}
		maxQuota, err := common.WalletQuotaFromDecimalStrict(decimal.NewFromInt(1_000_000_000).Mul(decimal.NewFromFloat(common.QuotaPerUnit)))
		if err != nil {
			maxQuota = common.MaxWalletQuota
		}
		if token.RemainQuota > maxQuota {
			return &TokenValidationError{Code: "quota_exceed_max", Details: map[string]any{"Max": maxQuota}}
		}
	}
	return nil
}

func applyTokenSettings(token *entity.Token, input contract.TokenSettings) {
	token.Name, token.ExpiredTime, token.RemainQuota = input.Name, input.ExpiredTime, input.RemainQuota
	token.UnlimitedQuota, token.ModelLimitsEnabled = input.UnlimitedQuota, input.ModelLimitsEnabled
	token.ModelLimits, token.AllowIps = value.StringList(input.ModelLimits), input.AllowIps
	token.Group, token.CrossGroupRetry = input.Group, input.CrossGroupRetry
}

func (s *Service) tokenActorGroup(ctx context.Context, actor contract.TokenActor) (string, error) {
	if actor.Group != "" {
		return actor.Group, nil
	}
	return s.tokenPolicy.UserGroup(ctx, actor.ID)
}

func (s *Service) setTokenAutoGroups(ctx context.Context, actor contract.TokenActor, token *entity.Token, groups []string) error {
	if len(groups) == 0 {
		return token.SetAutoGroups(nil)
	}
	maxCount := s.tokenPolicy.MaxAutoGroups()
	if len(groups) > maxCount {
		return &TokenValidationError{Code: "auto_groups_too_many", Details: map[string]any{"Max": maxCount}}
	}
	userGroup, err := s.tokenActorGroup(ctx, actor)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if _, ok := seen[group]; ok {
			return &TokenValidationError{Code: "auto_groups_duplicate", Details: map[string]any{"Group": group}}
		}
		seen[group] = struct{}{}
		if !s.tokenPolicy.IsSelectableGroup(userGroup, group) {
			return &TokenValidationError{Code: "auto_groups_invalid", Details: map[string]any{"Group": group}}
		}
	}
	return token.SetAutoGroups(groups)
}

func maskedTokens(tokens []*entity.Token) []*contract.TokenResponse {
	result := make([]*contract.TokenResponse, 0, len(tokens))
	for _, token := range tokens {
		result = append(result, maskedToken(token))
	}
	return result
}

func maskedToken(token *entity.Token) *contract.TokenResponse {
	autoGroups, _ := token.GetAutoGroups()
	if len(autoGroups) == 0 {
		autoGroups = nil
	}
	result := &contract.TokenResponse{Id: token.Id, UserId: token.UserId, Key: token.GetMaskedKey(), Status: token.Status, Name: token.Name,
		CreatedTime: token.CreatedTime, AccessedTime: token.AccessedTime, ExpiredTime: token.ExpiredTime, RemainQuota: token.RemainQuota,
		UnlimitedQuota: token.UnlimitedQuota, ModelLimitsEnabled: token.ModelLimitsEnabled, ModelLimits: token.GetModelLimits(), AllowIps: token.AllowIps,
		UsedQuota: token.UsedQuota, Group: token.Group, CrossGroupRetry: token.CrossGroupRetry, AutoGroups: autoGroups}
	if token.DeletedAt.Valid {
		result.DeletedAt = &token.DeletedAt.Time
	}
	return result
}

// TokenNames resolves current labels; soft-deleted credentials stay unnamed.
func (s *Service) TokenNames(ctx context.Context, ids []int) (map[int]string, error) {
	return s.tokens.Names(ctx, ids)
}
