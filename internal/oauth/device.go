package oauth

import (
	"time"
)

// DefaultDeviceCodeLifetime bounds a device authorization that omits expires_in,
// so consumers always apply a bounded expiry gate (fail closed). It is
// exported because the QG device flow shares this bound instead of restating it.
const DefaultDeviceCodeLifetime = 10 * time.Minute

// DeviceAuth is the result of an RFC 8628 device-authorization request.
type DeviceAuth struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	Interval                time.Duration
	ExpiresAt               time.Time
}
