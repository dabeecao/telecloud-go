package utils

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

var SecretKey []byte

func InitCrypto(adminPassword string) {
	SecretKey = []byte(adminPassword)
}

func GenerateDirectToken(shareToken string) string {
	h := hmac.New(sha256.New, SecretKey)
	h.Write([]byte(shareToken))
	sig := hex.EncodeToString(h.Sum(nil))
	if len(sig) > 32 {
		sig = sig[:32]
	}
	return shareToken + "_" + sig
}

func VerifyDirectToken(directToken string) *string {
	parts := strings.SplitN(directToken, "_", 2)
	if len(parts) != 2 {
		return nil
	}
	shareToken := parts[0]
	signature := parts[1]

	expected := GenerateDirectToken(shareToken)
	expectedSig := strings.SplitN(expected, "_", 2)[1]

	if hmac.Equal([]byte(signature), []byte(expectedSig)) {
		return &shareToken
	}
	return nil
}
