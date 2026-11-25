// Package certs checks the certificates an owner brings (feature 034): a PEM
// chain and its private key for an upload, and the pieces of a custom ACME
// issuer (its directory address, EAB key and CA certificate). It is pure: only
// the standard library, no Nomad, no store and no service, and the one network
// call, reading an ACME directory, goes through the http.Client the caller
// gives.
//
// Nothing here ever puts key bytes, an HMAC key or a response body in an error:
// a user-fixable refusal is an *Error that names the field at fault.
package certs

// Error is a refusal the owner can fix: Field is the request field at fault
// ("certificate", "key", "server", "eabKeyId", "eabHmacKey", "caCertificate")
// and Message says what is wrong in plain words.
type Error struct {
	Field   string
	Message string
}

func (e *Error) Error() string { return e.Field + ": " + e.Message }

func fieldError(field, message string) *Error { return &Error{Field: field, Message: message} }
