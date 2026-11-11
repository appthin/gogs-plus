package cryptox

import (
	"crypto/hmac"
	"crypto/sha256"
)

// HMACSHA256 returns the HMAC-SHA256 MAC of the data under the given key.
func HMACSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}
