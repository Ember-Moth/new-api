package identity_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/legacy/model"
	"github.com/QuantumNous/new-api/internal/legacy/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionManagementRefreshListAndRevocation(t *testing.T) {
	f := newUserFixture(t)
	user := seedManagedUser(t, f, "session-managed", common.RoleCommonUser)
	user.Password = "stored-private-hash"
	user.Remark = "private-admin-note"
	user.SetAccessToken("private-personal-access-token")
	require.NoError(t, f.db.Save(user).Error)
	runtime := f.service.Authentication
	first, err := runtime.CreateLoginSession(user.Id, "password", "127.0.0.1", "first-browser")
	require.NoError(t, err)
	second, err := runtime.CreateLoginSession(user.Id, "password", "127.0.0.1", "second-browser")
	require.NoError(t, err)
	list := selfRequest(t, f, first.AccessToken, http.MethodGet, "/self/sessions", nil)
	require.True(t, list.Success, list.Message)
	var sessions []contract.LoginSessionView
	require.NoError(t, common.Unmarshal(list.Data, &sessions))
	require.Len(t, sessions, 2)
	assert.True(t, sessions[0].Current)
	assert.Equal(t, first.Session.SID, sessions[0].SID)
	request := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	request.AddCookie(&http.Cookie{Name: service.RefreshCookieName, Value: first.RefreshToken})
	request.Header.Set("X-Auth-Session", first.Session.SID)
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var refreshed struct {
		Success bool `json:"success"`
		Data    struct {
			contract.AuthBundle
			User contract.SelfUserResponse `json:"user"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &refreshed))
	require.True(t, refreshed.Success, response.Body.String())
	assert.Equal(t, user.Id, refreshed.Data.User.Id)
	assert.NotContains(t, response.Body.String(), user.Password)
	assert.NotContains(t, response.Body.String(), user.Remark)
	assert.NotContains(t, response.Body.String(), user.GetAccessToken())
	var refreshCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == service.RefreshCookieName {
			refreshCookie = cookie
		}
	}
	require.NotNil(t, refreshCookie)
	assert.NotEqual(t, first.RefreshToken, refreshCookie.Value)
	assert.True(t, refreshCookie.HttpOnly)
	old, err := service.ParseAccessToken(first.AccessToken)
	require.NoError(t, err)
	_, _, err = runtime.ValidateLoginSession(old)
	require.NoError(t, err, "ordinary refresh preserves already issued access tokens until expiry or revocation")
	removed := selfRequest(t, f, refreshed.Data.AccessToken, http.MethodPost, "/self/sessions/revoke-others", nil)
	require.True(t, removed.Success, removed.Message)
	assert.JSONEq(t, `{"revoked_count":1}`, string(removed.Data))
	secondIdentity, err := service.ParseAccessToken(second.AccessToken)
	require.NoError(t, err)
	_, _, err = runtime.ValidateLoginSession(secondIdentity)
	require.Error(t, err)
	logout := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	logout.Header.Set("Authorization", "Bearer "+refreshed.Data.AccessToken)
	logout.Header.Set("X-Auth-Session", first.Session.SID)
	logout.AddCookie(refreshCookie)
	loggedOut := httptest.NewRecorder()
	f.router.ServeHTTP(loggedOut, logout)
	assert.Equal(t, http.StatusOK, loggedOut.Code)
	assert.Contains(t, loggedOut.Body.String(), `"success":true`)
	current, err := service.ParseAccessToken(refreshed.Data.AccessToken)
	require.NoError(t, err)
	_, _, err = runtime.ValidateLoginSession(current)
	require.Error(t, err)
	var stored model.UserSession
	require.NoError(t, f.db.First(&stored, "sid = ?", first.Session.SID).Error)
	assert.Equal(t, model.UserSessionStatusRevoked, stored.Status)
}
