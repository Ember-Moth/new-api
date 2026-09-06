package model

import (
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/constant"
	identityentity "github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/usercache"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
)

const userCacheSchemaVersion = usercache.SchemaVersion

type UserBase identityentity.UserBase

func getUserCacheKey(id int) string     { return usercache.CacheKey(id) }
func userCacheTTLSeconds() int          { return usercache.CacheTTLSeconds() }
func InvalidateUserCache(id int) error  { return usercache.New(DB).InvalidateUserCache(id) }
func populateUserCache(user User) error { return usercache.New(DB).Populate(identityentity.User(user)) }
func updateUserCache(user User) error   { return usercache.New(DB).Publish(identityentity.User(user)) }
func GetUserCache(id int) (*UserBase, error) {
	user, err := usercache.New(DB).GetUserCache(id)
	return (*UserBase)(user), err
}
func cacheGetUserBase(id int) (*UserBase, error) {
	user, err := usercache.New(DB).Cached(id)
	return (*UserBase)(user), err
}
func RefreshUserGroupCache(id int) error { return usercache.New(DB).RefreshUserGroupCache(id) }
func updateUserCacheField(id int, field string, value any) error {
	return usercache.New(DB).UpdateField(id, field, value)
}
func (user *UserBase) WriteContext(c *gin.Context) {
	common.SetContextKey(c, constant.ContextKeyUserGroup, user.Group)
	common.SetContextKey(c, constant.ContextKeyUserQuota, user.Quota)
	common.SetContextKey(c, constant.ContextKeyUserStatus, user.Status)
	common.SetContextKey(c, constant.ContextKeyUserEmail, user.Email)
	common.SetContextKey(c, constant.ContextKeyUserName, user.Username)
	common.SetContextKey(c, constant.ContextKeyUserSetting, user.GetSetting())
}

func (user *UserBase) GetSetting() dto.UserSetting {
	return (*identityentity.UserBase)(user).GetSetting()
}

// Helper functions to get individual fields if needed
func getUserGroupCache(userId int) (string, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return "", err
	}
	return cache.Group, nil
}

func getUserQuotaCache(userId int) (int, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return 0, err
	}
	return cache.Quota, nil
}

func getUserNameCache(userId int) (string, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return "", err
	}
	return cache.Username, nil
}

func getUserSettingCache(userId int) (dto.UserSetting, error) {
	cache, err := GetUserCache(userId)
	if err != nil {
		return dto.UserSetting{}, err
	}
	return cache.GetSetting(), nil
}

func updateUserEmailCache(userId int, email string) error {
	return updateUserCacheField(userId, "Email", email)
}

func updateUserNameCache(userId int, username string) error {
	return updateUserCacheField(userId, "Username", username)
}

func updateUserSettingCache(userId int, setting string) error {
	return updateUserCacheField(userId, "Setting", setting)
}

// GetUserLanguage returns the user's language preference from cache
// Uses the existing GetUserCache mechanism for efficiency
func GetUserLanguage(userId int) string {
	userCache, err := GetUserCache(userId)
	if err != nil {
		return ""
	}
	return userCache.GetSetting().Language
}
