// Package authz exposes identity's authorization contracts and engine.
package authz

import (
	"context"

	implementation "github.com/QuantumNous/new-api/internal/module/identity/internal/authorization"
	"gorm.io/gorm"
)

type Engine = implementation.Engine
type Permission = implementation.Permission
type PermissionsMap = implementation.PermissionsMap
type ResourceDefinition = implementation.ResourceDefinition
type RoleDescriptor = implementation.RoleDescriptor

const (
	ResourceChannel      = implementation.ResourceChannel
	ResourceTaskPlugin   = implementation.ResourceTaskPlugin
	ActionRead           = implementation.ActionRead
	ActionOperate        = implementation.ActionOperate
	ActionWrite          = implementation.ActionWrite
	ActionSensitiveWrite = implementation.ActionSensitiveWrite
	ActionSecretView     = implementation.ActionSecretView
	ActionBind           = implementation.ActionBind
)

var (
	ChannelRead           = implementation.ChannelRead
	ChannelOperate        = implementation.ChannelOperate
	ChannelWrite          = implementation.ChannelWrite
	ChannelSensitiveWrite = implementation.ChannelSensitiveWrite
	ChannelSecretView     = implementation.ChannelSecretView
	TaskPluginBind        = implementation.TaskPluginBind
)

func New(db *gorm.DB, master bool) (*Engine, error) { return implementation.New(db, master) }

func Catalog() []ResourceDefinition { return implementation.Catalog() }

func Roles() []RoleDescriptor { return implementation.Roles() }

func UserSubject(id int) string { return implementation.UserSubject(id) }

func WithEngine(ctx context.Context, engine *Engine) context.Context {
	return implementation.WithEngine(ctx, engine)
}

func FromContext(ctx context.Context) *Engine { return implementation.FromContext(ctx) }
