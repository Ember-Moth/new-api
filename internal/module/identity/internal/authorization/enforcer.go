package authz

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/casbin/casbin/v2"
	casbinmodel "github.com/casbin/casbin/v2/model"
	"gorm.io/gorm"
)

// Engine owns the authorization policy snapshot for one application instance.
type Engine struct{ enforcer *casbin.SyncedEnforcer }

const modelText = `
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act, eft

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = r.sub == p.sub && r.obj == p.obj && r.act == p.act && p.eft == "allow"
`

func New(db *gorm.DB, master bool) (*Engine, error) {
	if master {
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := seedBuiltInRoles(tx); err != nil {
				return err
			}
			if err := resetBuiltInRolePolicies(tx); err != nil {
				return err
			}
			return seedDefaultPolicies(tx)
		}); err != nil {
			return nil, err
		}
	}
	m, err := casbinmodel.NewModelFromString(modelText)
	if err != nil {
		return nil, err
	}
	enforcer, err := casbin.NewSyncedEnforcer(m, newGormAdapter(db))
	if err != nil {
		return nil, err
	}
	enforcer.EnableAutoSave(true)
	return &Engine{enforcer: enforcer}, nil
}

func (a *Engine) currentEnforcer() *casbin.SyncedEnforcer {
	if a == nil {
		return nil
	}
	return a.enforcer
}

func (a *Engine) ReloadPolicy() error {
	if a == nil || a.enforcer == nil {
		return fmt.Errorf("authz enforcer is not initialized")
	}
	return a.enforcer.LoadPolicy()
}

// StartPolicySync refreshes this instance until application shutdown.
func (a *Engine) StartPolicySync(ctx context.Context, frequency int) {
	if frequency <= 0 {
		return
	}
	ticker := time.NewTicker(time.Duration(frequency) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.ReloadPolicy(); err != nil {
				common.SysError("failed to reload authz policy: " + err.Error())
			}
		}
	}
}

type contextKey struct{}

// WithEngine supplies the application instance to inbound request adapters.
func WithEngine(ctx context.Context, engine *Engine) context.Context {
	return context.WithValue(ctx, contextKey{}, engine)
}

func FromContext(ctx context.Context) *Engine {
	engine, _ := ctx.Value(contextKey{}).(*Engine)
	return engine
}
