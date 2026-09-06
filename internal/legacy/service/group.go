package service

import (
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/constant"
	identitygroups "github.com/QuantumNous/new-api/internal/module/identity/groups"
	"github.com/QuantumNous/new-api/internal/legacy/model"
	"github.com/gin-gonic/gin"
)

// GetRequestAutoGroups reads the token's explicit group snapshot from HTTP metadata.
func GetRequestAutoGroups(c *gin.Context, userGroup string) []string {
	value, ok := common.GetContextKey(c, constant.ContextKeyTokenAutoGroups)
	if !ok {
		return identitygroups.GetUserAutoGroup(userGroup)
	}
	groups, ok := value.([]string)
	if !ok {
		return []string{}
	}
	return identitygroups.FilterUserTokenAutoGroups(userGroup, groups)
}

func GetGroupsEnabledModels(groups []string) []string {
	seen := make(map[string]struct{})
	models := make([]string, 0)
	for _, group := range groups {
		for _, modelName := range model.GetGroupEnabledModels(group) {
			if _, ok := seen[modelName]; !ok {
				seen[modelName] = struct{}{}
				models = append(models, modelName)
			}
		}
	}
	return models
}
