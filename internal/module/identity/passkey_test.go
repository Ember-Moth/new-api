package identity_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/legacy/model"
	"github.com/QuantumNous/new-api/internal/legacy/service"
	"github.com/QuantumNous/new-api/internal/config/setting/system_setting"
	"github.com/fxamacker/cbor/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type browserAuthenticator struct {
	key    *ecdsa.PrivateKey
	id     []byte
	userID int
}

func (a browserAuthenticator) registration(t *testing.T, challenge string) any {
	t.Helper()
	client, err := common.Marshal(map[string]any{"type": "webauthn.create", "challenge": challenge, "origin": "https://example.test", "crossOrigin": false})
	require.NoError(t, err)
	cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: a.key.X.FillBytes(make([]byte, 32)), -3: a.key.Y.FillBytes(make([]byte, 32))})
	require.NoError(t, err)
	rp := sha256.Sum256([]byte("example.test"))
	auth := append([]byte{}, rp[:]...)
	auth = append(auth, 0x45)
	auth = binary.BigEndian.AppendUint32(auth, 0)
	auth = append(auth, make([]byte, 16)...)
	auth = binary.BigEndian.AppendUint16(auth, uint16(len(a.id)))
	auth = append(auth, a.id...)
	auth = append(auth, cose...)
	attestation, err := cbor.Marshal(map[string]any{"fmt": "none", "authData": auth, "attStmt": map[string]any{}})
	require.NoError(t, err)
	rawID := base64.RawURLEncoding.EncodeToString(a.id)
	return map[string]any{"id": rawID, "rawId": rawID, "type": "public-key", "authenticatorAttachment": "platform",
		"response":               map[string]any{"clientDataJSON": base64.RawURLEncoding.EncodeToString(client), "attestationObject": base64.RawURLEncoding.EncodeToString(attestation), "transports": []string{"internal"}},
		"clientExtensionResults": map[string]any{"credProps": map[string]any{"rk": true}},
	}
}
func (a browserAuthenticator) assertion(t *testing.T, challenge string, count uint32) any {
	t.Helper()
	client, err := common.Marshal(map[string]any{"type": "webauthn.get", "challenge": challenge, "origin": "https://example.test", "crossOrigin": false})
	require.NoError(t, err)
	rp := sha256.Sum256([]byte("example.test"))
	auth := append([]byte{}, rp[:]...)
	auth = append(auth, 0x05)
	auth = binary.BigEndian.AppendUint32(auth, count)
	clientHash := sha256.Sum256(client)
	signed := append(append([]byte{}, auth...), clientHash[:]...)
	digest := sha256.Sum256(signed)
	signature, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	require.NoError(t, err)
	rawID := base64.RawURLEncoding.EncodeToString(a.id)
	return map[string]any{"id": rawID, "rawId": rawID, "type": "public-key", "response": map[string]any{
		"clientDataJSON": base64.RawURLEncoding.EncodeToString(client), "authenticatorData": base64.RawURLEncoding.EncodeToString(auth),
		"signature": base64.RawURLEncoding.EncodeToString(signature), "userHandle": base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(a.userID))),
	}}
}

type passkeyOptions struct {
	FlowToken string `json:"flow_token"`
	Options   struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	} `json:"options"`
}

func TestPasskeyRegistrationLoginVerificationAndDeletion(t *testing.T) {
	f := newUserFixture(t)
	settings := system_setting.GetPasskeySettings()
	previous := *settings
	*settings = system_setting.PasskeySettings{Enabled: true, RPID: "example.test", Origins: "https://example.test", RPDisplayName: "Test"}
	t.Cleanup(func() { *settings = previous })
	user := seedManagedUser(t, f, "passkey-lifecycle", common.RoleCommonUser)
	initial, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "browser")
	require.NoError(t, err)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	device := browserAuthenticator{key: key, id: []byte("test-credential-id"), userID: user.Id}
	started := selfRequest(t, f, initial.AccessToken, http.MethodPost, "/self/passkey/register/begin", nil)
	require.True(t, started.Success, started.Message)
	var creation passkeyOptions
	require.NoError(t, common.Unmarshal(started.Data, &creation))
	require.NotEmpty(t, creation.Options.PublicKey.Challenge)
	finished := selfRequest(t, f, initial.AccessToken, http.MethodPost, "/self/passkey/register/finish", map[string]any{
		"flow_token": creation.FlowToken, "credential": device.registration(t, creation.Options.PublicKey.Challenge),
	})
	require.True(t, finished.Success, finished.Message)
	var registered contract.AuthBundle
	require.NoError(t, common.Unmarshal(finished.Data, &registered))
	require.NoError(t, f.db.First(user, user.Id).Error)
	assert.EqualValues(t, 2, user.AuthVersion)
	var stored entity.PasskeyCredential
	require.NoError(t, f.db.Where("user_id = ?", user.Id).First(&stored).Error)
	assert.Equal(t, base64.StdEncoding.EncodeToString(device.id), stored.CredentialID)
	status := selfRequest(t, f, registered.AccessToken, http.MethodGet, "/self/passkey", nil)
	require.True(t, status.Success, status.Message)
	assert.NotContains(t, string(status.Data), stored.PublicKey)
	assert.NotContains(t, string(status.Data), stored.CredentialID)
	// Completed registration challenges cannot be replayed, even with the new session token.
	replay := selfRequest(t, f, registered.AccessToken, http.MethodPost, "/self/passkey/register/finish", map[string]any{
		"flow_token": creation.FlowToken, "credential": device.registration(t, creation.Options.PublicKey.Challenge),
	})
	assert.False(t, replay.Success)
	loginBegin := selfRequest(t, f, "", http.MethodPost, "/passkey/login/begin", nil)
	require.True(t, loginBegin.Success, loginBegin.Message)
	var loginOptions passkeyOptions
	require.NoError(t, common.Unmarshal(loginBegin.Data, &loginOptions))
	login := selfRequest(t, f, "", http.MethodPost, "/passkey/login/finish", map[string]any{
		"flow_token": loginOptions.FlowToken, "credential": device.assertion(t, loginOptions.Options.PublicKey.Challenge, 1),
	})
	require.True(t, login.Success, login.Message)
	var authenticated contract.AuthBundle
	require.NoError(t, common.Unmarshal(login.Data, &authenticated))
	identity, err := service.ParseAccessToken(authenticated.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, user.Id, identity.UserID)
	verifyBegin := selfRequest(t, f, registered.AccessToken, http.MethodPost, "/self/passkey/verify/begin", map[string]string{"scope": contract.SecurityProofScopePasskeyDelete})
	require.True(t, verifyBegin.Success, verifyBegin.Message)
	var verification passkeyOptions
	require.NoError(t, common.Unmarshal(verifyBegin.Data, &verification))
	verified := selfRequest(t, f, registered.AccessToken, http.MethodPost, "/self/passkey/verify/finish", map[string]any{
		"flow_token": verification.FlowToken, "credential": device.assertion(t, verification.Options.PublicKey.Challenge, 2),
	})
	require.True(t, verified.Success, verified.Message)
	var proof struct {
		Token string `json:"proof_token"`
		Scope string `json:"scope"`
	}
	require.NoError(t, common.Unmarshal(verified.Data, &proof))
	assert.Equal(t, contract.SecurityProofScopePasskeyDelete, proof.Scope)
	require.NoError(t, f.db.First(&stored, stored.ID).Error)
	assert.Equal(t, uint32(2), stored.SignCount)
	require.NotNil(t, stored.LastUsedAt)
	request := httptest.NewRequest(http.MethodDelete, "/self/passkey", strings.NewReader(""))
	request.Header.Set("Authorization", "Bearer "+registered.AccessToken)
	request.Header.Set("X-Security-Proof", proof.Token)
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), `"success":true`)
	require.NoError(t, f.db.First(user, user.Id).Error)
	assert.EqualValues(t, 3, user.AuthVersion)
	var count int64
	require.NoError(t, f.db.Model(&entity.PasskeyCredential{}).Where("user_id = ?", user.Id).Count(&count).Error)
	assert.Zero(t, count)
	_, _, err = service.ValidateLoginSession(identity)
	require.Error(t, err)
	// The removed credential cannot resolve an account through the legacy authentication bridge.
	_, err = model.GetPasskeyByCredentialID(device.id)
	require.Error(t, err)
}

func TestPasskeyReplacementRollsBackAndAdminResetProtectsPeers(t *testing.T) {
	f := newUserFixture(t)
	target := seedManagedUser(t, f, "passkey-admin", common.RoleAdminUser)
	other := seedManagedUser(t, f, "passkey-other", common.RoleCommonUser)
	original := entity.PasskeyCredential{UserID: target.Id, CredentialID: "original-credential", PublicKey: "original-public-key"}
	occupied := entity.PasskeyCredential{UserID: other.Id, CredentialID: "occupied-credential", PublicKey: "other-public-key"}
	require.NoError(t, f.db.Create(&original).Error)
	require.NoError(t, f.db.Create(&occupied).Error)
	login, err := service.CreateLoginSession(target.Id, "password", "127.0.0.1", "admin-browser")
	require.NoError(t, err)
	session, err := service.ParseAccessToken(login.AccessToken)
	require.NoError(t, err)
	_, err = f.service.SaveRegisteredPasskey(t.Context(), &entity.PasskeyCredential{UserID: target.Id, CredentialID: occupied.CredentialID, PublicKey: "replacement"}, session)
	require.Error(t, err)
	var kept entity.PasskeyCredential
	require.NoError(t, f.db.Where("user_id = ?", target.Id).First(&kept).Error)
	assert.Equal(t, original.CredentialID, kept.CredentialID)
	assert.Equal(t, original.PublicKey, kept.PublicKey)
	require.NoError(t, f.db.First(target, target.Id).Error)
	assert.EqualValues(t, 1, target.AuthVersion)
	_, err = f.service.AdminResetPasskey(t.Context(), contract.UserActor{ID: 9999, Role: common.RoleAdminUser}, target.Id)
	require.Error(t, err)
	_, err = f.service.AdminResetPasskey(t.Context(), contract.UserActor{ID: 9999, Role: common.RoleRootUser}, target.Id)
	require.NoError(t, err)
	require.NoError(t, f.db.First(target, target.Id).Error)
	assert.EqualValues(t, 2, target.AuthVersion)
	assert.Equal(t, []string{"admin_passkey_reset"}, f.revocations)
	_, _, err = service.ValidateLoginSession(session)
	require.Error(t, err)
}
