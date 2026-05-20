package common

import (
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type verificationValue struct {
	code string
	time time.Time
}

const (
	EmailVerificationPurpose = "v"
	PasswordResetPurpose     = "r"
)

var verificationMutex sync.Mutex
var verificationMap map[string]verificationValue
var verificationMapMaxSize = 10
var VerificationValidMinutes = 10

type VerificationCodeStore interface {
	RegisterVerificationCodeWithKey(key string, code string, purpose string) error
	VerifyCodeWithKey(key string, code string, purpose string) (bool, error)
	DeleteKey(key string, purpose string) error
}

var (
	verificationStoreMu sync.RWMutex
	verificationStore   VerificationCodeStore = memoryVerificationCodeStore{}
)

type memoryVerificationCodeStore struct{}

func SetVerificationCodeStore(store VerificationCodeStore) {
	if store == nil {
		return
	}
	verificationStoreMu.Lock()
	verificationStore = store
	verificationStoreMu.Unlock()
}

func currentVerificationCodeStore() VerificationCodeStore {
	verificationStoreMu.RLock()
	defer verificationStoreMu.RUnlock()
	return verificationStore
}

func GenerateVerificationCode(length int) string {
	code := uuid.New().String()
	code = strings.Replace(code, "-", "", -1)
	if length == 0 {
		return code
	}
	return code[:length]
}

func RegisterVerificationCodeWithKey(key string, code string, purpose string) {
	store := currentVerificationCodeStore()
	if err := store.RegisterVerificationCodeWithKey(key, code, purpose); err != nil {
		SysError("failed to register verification code: " + err.Error())
	}
}

func (memoryVerificationCodeStore) RegisterVerificationCodeWithKey(key string, code string, purpose string) error {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	verificationMap[purpose+key] = verificationValue{
		code: code,
		time: time.Now(),
	}
	if len(verificationMap) > verificationMapMaxSize {
		removeExpiredPairs()
	}
	return nil
}

func VerifyCodeWithKey(key string, code string, purpose string) bool {
	store := currentVerificationCodeStore()
	ok, err := store.VerifyCodeWithKey(key, code, purpose)
	if err != nil {
		SysError("failed to verify code: " + err.Error())
		return false
	}
	return ok
}

func (memoryVerificationCodeStore) VerifyCodeWithKey(key string, code string, purpose string) (bool, error) {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	value, okay := verificationMap[purpose+key]
	now := time.Now()
	if !okay || int(now.Sub(value.time).Seconds()) >= VerificationValidMinutes*60 {
		return false, nil
	}
	return code == value.code, nil
}

func DeleteKey(key string, purpose string) {
	store := currentVerificationCodeStore()
	if err := store.DeleteKey(key, purpose); err != nil {
		SysError("failed to delete verification code: " + err.Error())
	}
}

func (memoryVerificationCodeStore) DeleteKey(key string, purpose string) error {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	delete(verificationMap, purpose+key)
	return nil
}

// no lock inside, so the caller must lock the verificationMap before calling!
func removeExpiredPairs() {
	now := time.Now()
	for key := range verificationMap {
		if int(now.Sub(verificationMap[key].time).Seconds()) >= VerificationValidMinutes*60 {
			delete(verificationMap, key)
		}
	}
}

func init() {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	verificationMap = make(map[string]verificationValue)
	if verificationStore == nil {
		verificationStore = memoryVerificationCodeStore{}
	}
}
