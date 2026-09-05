package authentication

import (
	"github.com/QuantumNous/new-api/internal/module/identity/entity"
	"github.com/QuantumNous/new-api/internal/module/identity/internal/sessions"
)

type Dependencies struct {
	GetUserCache        func(int) (*entity.UserBase, error)
	GetUserById         func(int, bool) (*entity.User, error)
	BumpUserAuthVersion func(int) (int64, error)
	Sessions            *sessions.Store
}
type Runtime struct{ deps Dependencies }

func New(deps Dependencies) *Runtime { return &Runtime{deps: deps} }
