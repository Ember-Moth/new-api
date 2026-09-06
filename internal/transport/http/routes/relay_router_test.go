package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/internal/legacy/model"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModelsSupportsOpenAIAndGeminiAuthentication(t *testing.T) {
	setupRelayRouterTestDB(t)

	user := model.User{
		Username: "models-user",
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    100,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         user.Id,
		Key:            "modelstestkey",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	tests := []struct {
		name           string
		path           string
		headerName     string
		expectedObject string
		expectedField  string
	}{
		{
			name:           "OpenAI bearer token",
			path:           "/v1/models",
			headerName:     "Authorization",
			expectedObject: "list",
			expectedField:  "data",
		},
		{
			name:          "Gemini API key header",
			path:          "/v1/models",
			headerName:    "x-goog-api-key",
			expectedField: "models",
		},
		{
			name:          "Gemini API key query",
			path:          "/v1/models?key=modelstestkey",
			expectedField: "models",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.headerName != "" {
				value := "modelstestkey"
				if test.headerName == "Authorization" {
					value = "Bearer " + value
				}
				request.Header.Set(test.headerName, value)
			}

			engine.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code)
			var payload map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Contains(t, payload, test.expectedField)
			assert.NotContains(t, payload, "error")
			if test.expectedObject != "" {
				assert.Equal(t, test.expectedObject, payload["object"])
			}
		})
	}
}

func setupRelayRouterTestDB(t *testing.T) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	originalIsControlPlane := common.IsControlPlane
	originalRedisEnabled := common.RedisEnabled
	originalMainDatabaseType := common.MainDatabaseType()
	originalSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")

	common.IsControlPlane = false
	common.RedisEnabled = false
	common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
	require.NoError(t, os.Setenv("SQL_DSN", testdb.DSN(t)))
	require.NoError(t, model.InitDB())
	model.LOG_DB = testdb.Logs(t, model.DB)
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Token{}, &model.Ability{}))

	t.Cleanup(func() {
		if sqlDB, err := model.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		common.IsControlPlane = originalIsControlPlane
		common.RedisEnabled = originalRedisEnabled
		common.SetMainDatabaseType(originalMainDatabaseType)
		if hadSQLDSN {
			require.NoError(t, os.Setenv("SQL_DSN", originalSQLDSN))
		} else {
			require.NoError(t, os.Unsetenv("SQL_DSN"))
		}
	})
}

// Exercise the complete route composition: a disabled surface must never reach
// authentication, application handlers, or the embedded dashboard.
func TestApplicationPlaneRouteIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("FRONTEND_BASE_URL", "https://dashboard.example.test")
	t.Setenv("TRUSTED_PROXIES", "none")
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedis })
	for _, control := range []bool{true, false} {
		name := "data"
		if control {
			name = "control"
		}
		t.Run(name, func(t *testing.T) {
			engine := gin.New()
			SetRouter(engine, WebAssets{}, Dependencies{ControlPlane: control})
			for _, tc := range []struct {
				method, path              string
				controlStatus, dataStatus int
			}{
				{http.MethodGet, "/healthz", 200, 200},
				{http.MethodGet, "/api/user/self", 401, 404},
				{http.MethodPost, "/api/channel/", 401, 404},
				{http.MethodPost, "/v1/chat/completions", 404, 401},
				{http.MethodPost, "/v1/responses", 404, 401},
				{http.MethodPost, "/v1/tasks/example", 404, 401},
				{http.MethodPost, "/v1beta/models/gemini:generateContent", 404, 401},
				{http.MethodPost, "/mj/submit/imagine", 404, 401},
				{http.MethodPost, "/pg/chat/completions", 404, 401},
				{http.MethodGet, "/", 301, 404},
			} {
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
				want := tc.dataStatus
				if control {
					want = tc.controlStatus
				}
				assert.Equal(t, want, recorder.Code, "%s %s: %s", tc.method, tc.path, recorder.Body.String())
			}
		})
	}
}
