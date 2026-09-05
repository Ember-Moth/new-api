package authz

import (
	identityentity "github.com/QuantumNous/new-api/internal/module/identity/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func seedBuiltInRoles(db *gorm.DB) error {
	for _, spec := range builtInRoles {
		role := identityentity.AuthzRole{
			Key:         spec.Key,
			Name:        spec.Name,
			Description: spec.Description,
			BuiltIn:     spec.BuiltIn,
			Enabled:     true,
			Sort:        spec.Sort,
		}
		if err := db.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"name",
				"description",
				"built_in",
				"enabled",
				"sort",
			}),
		}).Create(&role).Error; err != nil {
			return err
		}
	}
	return nil
}

func resetBuiltInRolePolicies(db *gorm.DB) error {
	subjects := make([]string, 0, len(builtInRoles))
	for _, spec := range builtInRoles {
		subjects = append(subjects, RoleSubject(spec.Key))
	}
	return db.Where("ptype = ? AND v0 IN ?", "p", subjects).Delete(&identityentity.CasbinRule{}).Error
}

func seedDefaultPolicies(db *gorm.DB) error {
	for _, spec := range builtInRoles {
		if spec.Superuser {
			continue
		}
		for _, permission := range PermissionsForRole(spec.Key) {
			rule := newRule("p", []string{RoleSubject(spec.Key), permission.Resource, permission.Action, EffectAllow})
			if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&rule).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
