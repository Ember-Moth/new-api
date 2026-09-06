package groups

import implementation "github.com/QuantumNous/new-api/internal/module/identity/internal/groups"

func GetUserUsableGroups(group string) map[string]string {
	return implementation.GetUserUsableGroups(group)
}
func GroupInUserUsableGroups(user, group string) bool {
	return implementation.GroupInUserUsableGroups(user, group)
}
func IsUserSelectableGroup(user, group string) bool {
	return implementation.IsUserSelectableGroup(user, group)
}
func GetUserAutoGroup(group string) []string { return implementation.GetUserAutoGroup(group) }
func FilterUserTokenAutoGroups(group string, values []string) []string {
	return implementation.FilterUserTokenAutoGroups(group, values)
}
func GetUserGroupRatio(user, group string) float64 {
	return implementation.GetUserGroupRatio(user, group)
}
