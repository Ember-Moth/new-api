package model

import (
	"sync"

	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/subscription/memberships"
)

var subscriptionMembershipStores sync.Map

func SubscriptionMemberships() *memberships.Store {
	if cached, ok := subscriptionMembershipStores.Load(DB); ok {
		return cached.(*memberships.Store)
	}
	accounts := identity.New(identity.Dependencies{DB: DB})
	store := memberships.New(memberships.Dependencies{DB: DB, Plan: getSubscriptionPlanByIdTx, Groups: memberships.UserGroups{Lock: accounts.LockUserGroup, Set: accounts.SetUserGroup, Refresh: RefreshUserGroupCache}})
	actual, _ := subscriptionMembershipStores.LoadOrStore(DB, store)
	return actual.(*memberships.Store)
}
