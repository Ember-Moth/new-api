package authentication

import (
	"time"

	"github.com/QuantumNous/new-api/internal/module/identity/entity"
)

type Dependencies struct {
	GetUserCache                   func(int) (*entity.UserBase, error)
	GetUserById                    func(int, bool) (*entity.User, error)
	BumpUserAuthVersion            func(int) (int64, error)
	CountActiveUserSessions        func(int, int64) (int64, error)
	CountUserSessionsCreatedSince  func(int, int64) (int64, error)
	CreateUserSession              func(*entity.UserSession) error
	GetUserSessionCached           func(string) (*entity.UserSession, error)
	RevokeUserSession              func(int, string, string) (bool, error)
	AdvanceUserSessionAuthVersion  func(int, string, int64, int64, int64) (*entity.UserSession, error)
	RevokeOtherUserSessions        func(int, string, string) (int64, error)
	RotateUserSessionRefresh       func(int, string, string, string, int64, time.Duration) (*entity.UserSession, error)
	RevokeUserSessionByRefreshHash func(string, string, string) (bool, error)
	ListActiveUserSessions         func(int, string, int64) ([]entity.UserSession, error)
}
type Runtime struct{ deps Dependencies }

func New(deps Dependencies) *Runtime { return &Runtime{deps: deps} }
