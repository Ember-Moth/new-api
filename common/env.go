package common

import (
	"fmt"
	"os"
	"strconv"
)

func GetEnvOrDefault(env string, defaultValue int) int {
	// 优先从配置文件/Viper读取
	if Config != nil && Config.IsSet(env) {
		return Config.GetInt(env)
	}
	if env == "" || os.Getenv(env) == "" {
		return defaultValue
	}
	num, err := strconv.Atoi(os.Getenv(env))
	if err != nil {
		SysError(fmt.Sprintf("failed to parse %s: %s, using default value: %d", env, err.Error(), defaultValue))
		return defaultValue
	}
	return num
}

func GetEnvOrDefaultString(env string, defaultValue string) string {
	// 优先从配置文件/Viper读取
	if Config != nil && Config.IsSet(env) {
		return Config.GetString(env)
	}
	if env == "" || os.Getenv(env) == "" {
		return defaultValue
	}
	return os.Getenv(env)
}

func GetEnvOrDefaultBool(env string, defaultValue bool) bool {
	// 优先从配置文件/Viper读取
	if Config != nil && Config.IsSet(env) {
		return Config.GetBool(env)
	}
	if env == "" || os.Getenv(env) == "" {
		return defaultValue
	}
	b, err := strconv.ParseBool(os.Getenv(env))
	if err != nil {
		SysError(fmt.Sprintf("failed to parse %s: %s, using default value: %t", env, err.Error(), defaultValue))
		return defaultValue
	}
	return b
}
