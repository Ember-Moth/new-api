package model

import (
	"github.com/QuantumNous/new-api/internal/module/identity/usercache"
	"gorm.io/gorm"
)

var ErrUserAuthCachePending = usercache.ErrUserAuthCachePending
var ErrUserAuthVersionConflict = usercache.ErrUserAuthVersionConflict

func getUserAuthFenceKey(id int) string   { return usercache.FenceKey(id) }
func getUserAuthVersionKey(id int) string { return usercache.VersionKey(id) }
func SetUserAuthVersionFence(id int, version int64) error {
	return usercache.New(DB).SetUserAuthVersionFence(id, version)
}
func PublishCommittedUserAuthVersion(id int, version int64) error {
	return usercache.New(DB).PublishCommittedUserAuthVersion(id, version)
}
func IncrementUserAuthVersionWithTx(tx *gorm.DB, id int) (int64, error) {
	return usercache.New(DB).IncrementUserAuthVersionWithTx(tx, id)
}
func BumpUserAuthVersion(id int) (int64, error) { return usercache.New(DB).BumpUserAuthVersion(id) }
func PublishUserAuthCache(id int) error         { return usercache.New(DB).PublishUserAuthCache(id) }
