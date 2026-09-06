package common

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

const (
	EmailVerificationPurpose = "v"
	PasswordResetPurpose     = "r"
)

var VerificationValidMinutes = 10

func GenerateVerificationCode(length int) string {
	code := uuid.New().String()
	code = strings.Replace(code, "-", "", -1)
	if length == 0 {
		return code
	}
	return code[:length]
}

// Neither email addresses nor the plaintext code are stored in DragonflyDB.
func verificationKey(key, purpose string) string {
	return "auth:verification:" + GenerateHMACWithKey([]byte("verification-key:"+CryptoSecret), purpose+":"+key)
}

func RegisterVerificationCodeWithKey(key, code, purpose string) error {
	if RDB == nil {
		return errors.New("DragonflyDB is required for verification codes")
	}
	if key == "" || code == "" || purpose == "" || VerificationValidMinutes <= 0 {
		return errors.New("invalid verification code configuration")
	}
	cacheKey := verificationKey(key, purpose)
	digest := GenerateHMACWithKey([]byte("verification-code:"+CryptoSecret), cacheKey+":"+code)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return RDB.Set(ctx, cacheKey, digest, time.Duration(VerificationValidMinutes)*time.Minute).Err()
}

var consumeVerificationCode = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
return redis.call('DEL', KEYS[1])
`)

// VerifyCodeWithKey consumes a matching code exactly once. Wrong codes leave
// the challenge intact; cache failures reject the verification. A subsequent
// database failure requires a new code, rather than rearming a consumed secret.
func VerifyCodeWithKey(key, code, purpose string) bool {
	if RDB == nil || key == "" || code == "" || purpose == "" {
		return false
	}
	cacheKey := verificationKey(key, purpose)
	digest := GenerateHMACWithKey([]byte("verification-code:"+CryptoSecret), cacheKey+":"+code)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	consumed, err := consumeVerificationCode.Run(ctx, RDB, []string{cacheKey}, digest).Int64()
	if err != nil {
		SysError("failed to consume verification code: " + err.Error())
		return false
	}
	return consumed == 1
}
