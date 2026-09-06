package model

import (
	"sync"

	"github.com/QuantumNous/new-api/internal/module/identity"
	"github.com/QuantumNous/new-api/internal/module/usage/aggregation"
)

var quotaDataStores sync.Map

// QuotaDataStore shares one buffer across legacy log producers and the app's
// dashboard reader/flush worker. Remove this bridge with the legacy producers.
func QuotaDataStore() *aggregation.Store {
	if value, ok := quotaDataStores.Load(DB); ok {
		return value.(*aggregation.Store)
	}
	store := aggregation.New(aggregation.Dependencies{DB: DB, ChannelNames: ChannelService().ChannelNames, TokenNames: identity.New(identity.Dependencies{DB: DB}).TokenNames})
	actual, _ := quotaDataStores.LoadOrStore(DB, store)
	return actual.(*aggregation.Store)
}
