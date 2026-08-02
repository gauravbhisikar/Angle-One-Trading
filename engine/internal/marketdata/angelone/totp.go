package angelone

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"fmt"
	"strings"
	"time"
)

// GenerateTOTP implements RFC 6238 (30s step, 6 digits, SHA1) — the same
// algorithm Angel One's app-based 2FA uses. No external dependency needed.
func GenerateTOTP(secret string) (string, error) {
	secret = strings.ToUpper(strings.TrimSpace(secret))
	secret = strings.TrimRight(secret, "=")
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		return "", fmt.Errorf("angelone: invalid totp secret: %w", err)
	}

	counter := uint64(time.Now().Unix() / 30)
	buf := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		buf[i] = byte(counter & 0xff)
		counter >>= 8
	}

	mac := hmac.New(sha1.New, key)
	mac.Write(buf)
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	code := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])

	return fmt.Sprintf("%06d", code%1000000), nil
}
