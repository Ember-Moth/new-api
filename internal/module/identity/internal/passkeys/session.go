package passkeys

import (
	"errors"
	"time"

	"context"

	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/module/identity/internal/ceremony"

	webauthn "github.com/go-webauthn/webauthn/webauthn"
)

var errSessionNotFound = errors.New("Passkey 会话不存在或已过期")

const passkeyFlowTTL = 5 * time.Minute

type flowPayload struct {
	SessionData webauthn.SessionData `json:"session_data"`
	Scope       string               `json:"scope,omitempty"`
}

func (r *Flows) CreateSessionDataFlow(ctx context.Context, purpose string, userID int, sessionID, scope string, data *webauthn.SessionData) (string, int64, error) {
	if data == nil {
		return "", 0, errors.New("Passkey 会话数据不能为空")
	}
	payload, err := common.Marshal(flowPayload{SessionData: *data, Scope: scope})
	if err != nil {
		return "", 0, err
	}
	expiresAt := time.Now().Add(passkeyFlowTTL)
	token, _, err := r.store.CreateAuthFlow(ctx, ceremony.AuthFlowCreate{
		Purpose:   purpose,
		UserId:    userID,
		SessionId: sessionID,
		Payload:   string(payload),
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return "", 0, err
	}
	return token, expiresAt.Unix(), nil
}

func (r *Flows) PopSessionDataFlow(ctx context.Context, token, purpose string, userID int, sessionID string) (*webauthn.SessionData, string, error) {
	flow, err := r.store.ConsumeAuthFlow(ctx, token, ceremony.AuthFlowMatch{
		Purpose:   purpose,
		UserId:    userID,
		SessionId: sessionID,
	})
	if err != nil {
		if errors.Is(err, ceremony.ErrAuthFlowInvalid) || errors.Is(err, ceremony.ErrAuthFlowExpired) || errors.Is(err, ceremony.ErrAuthFlowConsumed) {
			return nil, "", errSessionNotFound
		}
		return nil, "", err
	}
	var payload flowPayload
	if err := common.UnmarshalJsonStr(flow.Payload, &payload); err != nil {
		return nil, "", err
	}
	return &payload.SessionData, payload.Scope, nil
}

type Flows struct{ store *ceremony.Flows }

func NewFlows(store *ceremony.Flows) *Flows { return &Flows{store: store} }
