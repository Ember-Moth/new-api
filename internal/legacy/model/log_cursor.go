package model

import "github.com/QuantumNous/new-api/internal/module/usage"

type LogCursorPage = usage.LogCursorPage

var ErrInvalidLogCursor = usage.ErrInvalidLogCursor

func NewLogCursorPage(encoded, scope string) (*LogCursorPage, error) {
	return LogService().NewLogCursorPage(encoded, scope)
}
