package contract

const (
	SecurityProofScopeChannelKeyRead  = "channel.key.read"
	SecurityProofScopePasskeyRegister = "passkey.register"
	SecurityProofScopePasskeyDelete   = "passkey.delete"
	SecurityVerificationMethod2FA     = "2fa"
	SecurityVerificationMethodPasskey = "passkey"
)

func AllowedSecurityProofScope(scope string) bool {
	switch scope {
	case SecurityProofScopeChannelKeyRead, SecurityProofScopePasskeyRegister, SecurityProofScopePasskeyDelete:
		return true
	default:
		return false
	}
}
