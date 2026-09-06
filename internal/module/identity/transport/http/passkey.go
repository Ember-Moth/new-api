package identityhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/identity/contract"
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	passkeysvc "github.com/QuantumNous/new-api/internal/module/identity/passkeys"
	"github.com/QuantumNous/new-api/internal/config/setting/system_setting"

	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	webauthnlib "github.com/go-webauthn/webauthn/webauthn"
)

const (
	securityProofScopeChannelKeyRead  = "channel.key.read"
	securityProofScopePasskeyRegister = "passkey.register"
	securityProofScopePasskeyDelete   = "passkey.delete"
)

type passkeyFinishRequest struct {
	FlowToken  string          `json:"flow_token"`
	Credential json.RawMessage `json:"credential"`
}

type passkeyVerifyBeginRequest struct {
	Scope string `json:"scope"`
}

func parsePasskeyFinishRequest(c *gin.Context) (*passkeyFinishRequest, error) {
	var request passkeyFinishRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		return nil, err
	}
	if request.FlowToken == "" || len(request.Credential) == 0 {
		return nil, errors.New("Passkey 流程参数不完整")
	}
	return &request, nil
}

func (h *Handler) PasskeyRegisterBegin(c *gin.Context) {
	if !system_setting.GetPasskeySettings().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	user, err := h.getAuthenticatedUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	if !h.requirePasskeyRegistrationVerification(c, user.Id) {
		return
	}

	credential, err := h.identity.PasskeyByUserID(c.Request.Context(), user.Id)
	if err != nil && !errors.Is(err, entity.ErrPasskeyNotFound) {
		common.ApiError(c, err)
		return
	}
	if errors.Is(err, entity.ErrPasskeyNotFound) {
		credential = nil
	}

	wa, err := passkeysvc.BuildWebAuthn(c.Request)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	waUser := passkeysvc.NewWebAuthnUser(user, credential)
	var options []webauthnlib.RegistrationOption
	if credential != nil {
		descriptor := credential.ToWebAuthnCredential().Descriptor()
		options = append(options, webauthnlib.WithExclusions([]protocol.CredentialDescriptor{descriptor}))
	}

	creation, sessionData, err := wa.BeginRegistration(waUser, options...)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	identity, ok := h.passkeySession(c)
	if !ok {
		common.ApiError(c, errors.New("当前认证方式不支持安全验证"))
		return
	}
	flowToken, expiresAt, err := h.identity.PasskeyFlow(c.Request.Context(),
		entity.AuthFlowPurposePasskeyRegister,
		user.Id,
		identity.SessionID,
		securityProofScopePasskeyRegister,
		sessionData,
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"options":    creation,
			"flow_token": flowToken,
			"expires_at": expiresAt,
		},
	})
}

func (h *Handler) PasskeyRegisterFinish(c *gin.Context) {
	if !system_setting.GetPasskeySettings().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	user, err := h.getAuthenticatedUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	if !h.requirePasskeyRegistrationVerification(c, user.Id) {
		return
	}

	request, err := parsePasskeyFinishRequest(c)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	parsedCredential, err := protocol.ParseCredentialCreationResponseBytes(request.Credential)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	wa, err := passkeysvc.BuildWebAuthn(c.Request)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	credentialRecord, err := h.identity.PasskeyByUserID(c.Request.Context(), user.Id)
	if err != nil && !errors.Is(err, entity.ErrPasskeyNotFound) {
		common.ApiError(c, err)
		return
	}
	if errors.Is(err, entity.ErrPasskeyNotFound) {
		credentialRecord = nil
	}

	identity, ok := h.passkeySession(c)
	if !ok {
		common.ApiError(c, errors.New("当前认证方式不支持安全验证"))
		return
	}
	sessionData, _, err := h.identity.ConsumePasskeyFlow(c.Request.Context(),
		request.FlowToken,
		entity.AuthFlowPurposePasskeyRegister,
		user.Id,
		identity.SessionID,
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	waUser := passkeysvc.NewWebAuthnUser(user, credentialRecord)
	credential, err := wa.CreateCredential(waUser, *sessionData, parsedCredential)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	passkeyCredential := entity.NewPasskeyCredentialFromWebAuthn(user.Id, credential)
	if passkeyCredential == nil {
		common.ApiErrorMsg(c, "无法创建 Passkey 凭证")
		return
	}

	bundle, err := h.identity.SaveRegisteredPasskey(c.Request.Context(), passkeyCredential, identity)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	h.passkeyAudit(c, user.Id, "user.passkey_register", nil)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Passkey 注册成功",
		"data":    bundle,
	})
}

func (h *Handler) PasskeyDelete(c *gin.Context) {
	user, err := h.getAuthenticatedUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	if !h.requirePasskeyDeleteVerification(c, user.Id) {
		return
	}

	identity, ok := h.passkeySession(c)
	if !ok {
		common.ApiError(c, errors.New("当前认证方式不支持安全验证"))
		return
	}
	bundle, err := h.identity.DeletePasskey(c.Request.Context(), user.Id, identity)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	h.passkeyAudit(c, user.Id, "user.passkey_delete", nil)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Passkey 已解绑",
		"data":    bundle,
	})
}

func (h *Handler) PasskeyStatus(c *gin.Context) {
	user, err := h.getAuthenticatedUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	credential, err := h.identity.PasskeyByUserID(c.Request.Context(), user.Id)
	if errors.Is(err, entity.ErrPasskeyNotFound) {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "",
			"data": gin.H{
				"enabled": false,
			},
		})
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}

	data := gin.H{
		"enabled":      true,
		"last_used_at": credential.LastUsedAt,
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    data,
	})
}

func (h *Handler) PasskeyLoginBegin(c *gin.Context) {
	if !system_setting.GetPasskeySettings().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	wa, err := passkeysvc.BuildWebAuthn(c.Request)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	assertion, sessionData, err := wa.BeginDiscoverableLogin()
	if err != nil {
		common.ApiError(c, err)
		return
	}

	flowToken, expiresAt, err := h.identity.PasskeyFlow(c.Request.Context(),
		entity.AuthFlowPurposePasskeyLogin,
		0,
		"",
		"",
		sessionData,
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"options":    assertion,
			"flow_token": flowToken,
			"expires_at": expiresAt,
		},
	})
}

func (h *Handler) PasskeyLoginFinish(c *gin.Context) {
	if !system_setting.GetPasskeySettings().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	request, err := parsePasskeyFinishRequest(c)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	parsedCredential, err := protocol.ParseCredentialRequestResponseBytes(request.Credential)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	wa, err := passkeysvc.BuildWebAuthn(c.Request)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	sessionData, _, err := h.identity.ConsumePasskeyFlow(c.Request.Context(),
		request.FlowToken,
		entity.AuthFlowPurposePasskeyLogin,
		0,
		"",
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	handler := func(rawID, userHandle []byte) (webauthnlib.User, error) {
		// 首先通过凭证ID查找用户
		credential, err := h.identity.PasskeyByCredentialID(c.Request.Context(), rawID)
		if err != nil {
			return nil, fmt.Errorf("未找到 Passkey 凭证: %w", err)
		}

		// 通过凭证获取用户
		user, err := h.identity.AuthenticatedPasskeyUser(c.Request.Context(), credential.UserID)
		if err != nil {
			return nil, fmt.Errorf("用户信息获取失败: %w", err)
		}

		if user.Status != common.UserStatusEnabled {
			return nil, errors.New("该用户已被禁用")
		}

		if len(userHandle) > 0 {
			userID, parseErr := strconv.Atoi(string(userHandle))
			if parseErr != nil {
				// 记录异常但继续验证，因为某些客户端可能使用非数字格式
				common.SysLog(fmt.Sprintf("PasskeyLogin: userHandle parse error for credential, length: %d", len(userHandle)))
			} else if userID != user.Id {
				return nil, errors.New("用户句柄与凭证不匹配")
			}
		}

		return passkeysvc.NewWebAuthnUser(user, credential), nil
	}

	waUser, credential, err := wa.ValidatePasskeyLogin(handler, *sessionData, parsedCredential)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	userWrapper, ok := waUser.(*passkeysvc.WebAuthnUser)
	if !ok {
		common.ApiErrorMsg(c, "Passkey 登录状态异常")
		return
	}

	modelUser := userWrapper.ModelUser()
	if modelUser == nil {
		common.ApiErrorMsg(c, "Passkey 登录状态异常")
		return
	}

	if modelUser.Status != common.UserStatusEnabled {
		common.ApiErrorMsg(c, "该用户已被禁用")
		return
	}

	if err := h.identity.RecordPasskeyAssertion(c.Request.Context(), modelUser.Id, credential, time.Now()); err != nil {
		common.ApiError(c, err)
		return
	}

	if h.hooks.PasskeyLogin == nil {
		common.ApiErrorMsg(c, "Passkey 登录状态异常")
		return
	}
	h.hooks.PasskeyLogin(c, modelUser)
}

func (h *Handler) AdminResetPasskey(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "无效的用户 ID")
		return
	}

	user, err := h.identity.AdminResetPasskey(c.Request.Context(), userActor(c), id)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	if h.hooks.Audit != nil {
		h.hooks.Audit(c, user.Id, "user.reset_passkey", map[string]any{"username": user.Username, "id": user.Id})
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Passkey 已重置",
	})
}

func (h *Handler) PasskeyVerifyBegin(c *gin.Context) {
	if !system_setting.GetPasskeySettings().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	user, err := h.getAuthenticatedUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	var request passkeyVerifyBeginRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiError(c, errors.New("无效的 Passkey 验证请求"))
		return
	}
	if !contract.AllowedSecurityProofScope(request.Scope) {
		common.ApiError(c, errors.New("不支持的安全验证范围"))
		return
	}

	credential, err := h.identity.PasskeyByUserID(c.Request.Context(), user.Id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "该用户尚未绑定 Passkey",
		})
		return
	}

	wa, err := passkeysvc.BuildWebAuthn(c.Request)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	waUser := passkeysvc.NewWebAuthnUser(user, credential)
	assertion, sessionData, err := wa.BeginLogin(waUser)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	identity, ok := h.passkeySession(c)
	if !ok {
		common.ApiError(c, errors.New("当前认证方式不支持安全验证"))
		return
	}
	flowToken, expiresAt, err := h.identity.PasskeyFlow(c.Request.Context(),
		entity.AuthFlowPurposePasskeyStepUp,
		user.Id,
		identity.SessionID,
		request.Scope,
		sessionData,
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"options":    assertion,
			"flow_token": flowToken,
			"expires_at": expiresAt,
		},
	})
}

func (h *Handler) PasskeyVerifyFinish(c *gin.Context) {
	if !system_setting.GetPasskeySettings().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	user, err := h.getAuthenticatedUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	request, err := parsePasskeyFinishRequest(c)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	parsedCredential, err := protocol.ParseCredentialRequestResponseBytes(request.Credential)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	wa, err := passkeysvc.BuildWebAuthn(c.Request)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	credential, err := h.identity.PasskeyByUserID(c.Request.Context(), user.Id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "该用户尚未绑定 Passkey",
		})
		return
	}

	identity, ok := h.passkeySession(c)
	if !ok {
		common.ApiError(c, errors.New("当前认证方式不支持安全验证"))
		return
	}
	sessionData, scope, err := h.identity.ConsumePasskeyFlow(c.Request.Context(),
		request.FlowToken,
		entity.AuthFlowPurposePasskeyStepUp,
		user.Id,
		identity.SessionID,
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	waUser := passkeysvc.NewWebAuthnUser(user, credential)
	validatedCredential, err := wa.ValidateLogin(waUser, *sessionData, parsedCredential)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	if err := h.identity.RecordPasskeyAssertion(c.Request.Context(), user.Id, validatedCredential, time.Now()); err != nil {
		common.ApiError(c, err)
		return
	}

	proofToken, proofExpiresAt, err := h.identity.IssueSecurityProof(identity, contract.SecurityVerificationMethodPasskey, []string{scope})
	if err != nil {
		common.ApiError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Passkey 验证成功",
		"data": gin.H{
			"proof_token": proofToken,
			"expires_at":  proofExpiresAt,
			"method":      contract.SecurityVerificationMethodPasskey,
			"scope":       scope,
		},
	})
}

func (h *Handler) getAuthenticatedUser(c *gin.Context) (*entity.User, error) {
	return h.identity.AuthenticatedPasskeyUser(c.Request.Context(), c.GetInt("id"))
}

func (h *Handler) requirePasskeyRegistrationVerification(c *gin.Context, userID int) bool {
	enabled, err := h.identity.PasskeyRequiresTwoFA(c.Request.Context(), userID)
	if err != nil {
		common.ApiError(c, err)
		return false
	}
	if !enabled {
		return true
	}
	return h.requirePasskeyProof(c, securityProofScopePasskeyRegister, []string{contract.SecurityVerificationMethod2FA})
}

func (h *Handler) requirePasskeyDeleteVerification(c *gin.Context, userID int) bool {
	enabled, err := h.identity.PasskeyRequiresTwoFA(c.Request.Context(), userID)
	if err != nil {
		common.ApiError(c, err)
		return false
	}
	if enabled {
		return h.requirePasskeyProof(c, securityProofScopePasskeyDelete, []string{contract.SecurityVerificationMethod2FA})
	}

	_, err = h.identity.PasskeyByUserID(c.Request.Context(), userID)
	if err != nil {
		if errors.Is(err, entity.ErrPasskeyNotFound) {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "该用户尚未绑定 Passkey",
			})
			return false
		}
		common.ApiError(c, err)
		return false
	}

	return h.requirePasskeyProof(c, securityProofScopePasskeyDelete, []string{contract.SecurityVerificationMethodPasskey})
}

func (h *Handler) passkeySession(c *gin.Context) (contract.AuthIdentity, bool) {
	if h.hooks.SessionIdentity == nil {
		return contract.AuthIdentity{}, false
	}
	return h.hooks.SessionIdentity(c)
}
func (h *Handler) requirePasskeyProof(c *gin.Context, scope string, methods []string) bool {
	if h.hooks.RequireSecurityProof == nil {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "code": "SECURITY_PROOF_REQUIRED", "message": "security proof required"})
		return false
	}
	return h.hooks.RequireSecurityProof(c, scope, methods)
}
func (h *Handler) passkeyAudit(c *gin.Context, id int, event string, data map[string]any) {
	if h.hooks.SecurityAudit != nil {
		h.hooks.SecurityAudit(c, id, event, data)
	}
}
