package contract

import "errors"

var (
	ErrUserSessionInvalid        = errors.New("user session is invalid")
	ErrUserSessionInactive       = errors.New("user session is inactive")
	ErrUserSessionRefreshInvalid = errors.New("user session refresh token is invalid")
	ErrUserSessionRefreshRace    = errors.New("user session refresh is already in progress")
	ErrUserSessionRefreshReuse   = errors.New("user session refresh token was reused")
	ErrUserSessionLimit          = errors.New("active user session limit reached")
	ErrUserSessionIssuanceLimit  = errors.New("user session issuance limit reached")
)
