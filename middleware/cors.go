package middleware

import (
	"net/url"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func CORS() gin.HandlerFunc {
	config := cors.DefaultConfig()
	config.AllowCredentials = true
	config.AllowMethods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}
	config.AllowHeaders = []string{"*"}
	if frontendOrigin := getFrontendOrigin(); frontendOrigin != "" {
		config.AllowOrigins = []string{frontendOrigin}
	} else {
		config.AllowAllOrigins = true
	}
	return cors.New(config)
}

func getFrontendOrigin() string {
	frontendBaseUrl := strings.TrimSpace(os.Getenv("FRONTEND_BASE_URL"))
	if frontendBaseUrl == "" {
		return ""
	}
	parsed, err := url.Parse(frontendBaseUrl)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimSuffix(frontendBaseUrl, "/")
	}
	return parsed.Scheme + "://" + parsed.Host
}

func PoweredBy() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-New-Api-Version", common.Version)
		c.Next()
	}
}
