package identity

import (
	"context"
	"sort"

	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/groups"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

func (s *Service) GroupNames() []string {
	names := make([]string, 0)
	for name := range ratio_setting.GetGroupRatioCopy() {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func (s *Service) UserGroupChoices(ctx context.Context, userID int) (map[string]contract.GroupChoice, error) {
	userGroup := ""
	if userID > 0 {
		user, err := s.users.Get(ctx, userID, false)
		if err != nil {
			return nil, err
		}
		userGroup = user.Group
	}
	usable := groups.GetUserUsableGroups(userGroup)
	choices := make(map[string]contract.GroupChoice)
	for name := range ratio_setting.GetGroupRatioCopy() {
		if desc, ok := usable[name]; ok {
			choices[name] = contract.GroupChoice{Ratio: groups.GetUserGroupRatio(userGroup, name), Description: desc}
		}
	}
	if _, ok := usable["auto"]; ok {
		choices["auto"] = contract.GroupChoice{Ratio: "自动", Description: setting.GetUsableGroupDescription("auto")}
	}
	return choices, nil
}
