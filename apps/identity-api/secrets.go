package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	tokenBytes        = 32
	passwordSaltBytes = 16
	passwordKeyBytes  = 32
	passwordFormat    = "$argon2id$v=19$m=19456,t=2,p=1$"
)

func normalizeEmail(value string) (string, bool) {
	email := strings.TrimSpace(value)
	if len(email) == 0 || len(email) > 254 || strings.ContainsAny(email, "<>,\r\n") {
		return "", false
	}
	for _, character := range email {
		if character < 33 || character > 126 {
			return "", false
		}
	}
	parsed, err := mail.ParseAddress(email)
	_, domain, at := strings.Cut(email, "@")
	if err != nil || parsed.Name != "" || parsed.Address != email || !at || !strings.Contains(domain, ".") {
		return "", false
	}
	return strings.ToLower(email), true
}

func normalizeName(value string) (string, bool) {
	name := strings.TrimSpace(value)
	if name == "" || utf8.RuneCountInString(name) > 60 || strings.ContainsFunc(name, unicode.IsControl) {
		return "", false
	}
	return name, true
}

func validPassword(password string) bool {
	length := utf8.RuneCountInString(password)
	return length >= 15 && length <= 128
}

func acceptableNewPassword(password string) bool {
	if !validPassword(password) {
		return false
	}
	canonical := strings.ToLower(strings.TrimSpace(password))
	switch canonical {
	case "", "123456789012345", "1234567890123456", "0123456789012345",
		"123456789123456789", "passwordpassword", "passwordpassword1",
		"password123456789", "qwertyuiopasdfghjkl", "qwerty1234567890",
		"qwertyqwertyqwerty", "iloveyouiloveyou", "letmeinletmeinletmein",
		"thisisapassword", "correcthorsebatterystaple", "correct horse battery staple",
		"kadrroomkadrroom", "парольпарольпароль":
		return false
	}
	first, _ := utf8.DecodeRuneInString(canonical)
	return strings.ContainsFunc(canonical, func(character rune) bool { return character != first })
}

func hashPassword(password string) (string, error) {
	var salt [passwordSaltBytes]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	hash := derivePassword(password, salt[:])
	return passwordFormat + base64.RawStdEncoding.EncodeToString(salt[:]) + "$" + base64.RawStdEncoding.EncodeToString(hash), nil
}

func verifyPassword(encoded, password string) bool {
	fields, valid := strings.CutPrefix(encoded, passwordFormat)
	saltValue, hashValue, split := strings.Cut(fields, "$")
	salt, saltErr := base64.RawStdEncoding.DecodeString(saltValue)
	hash, hashErr := base64.RawStdEncoding.DecodeString(hashValue)
	if !valid || !split || saltErr != nil || hashErr != nil || len(salt) != passwordSaltBytes || len(hash) != passwordKeyBytes {
		return false
	}
	return subtle.ConstantTimeCompare(derivePassword(password, salt), hash) == 1
}

func derivePassword(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, passwordKeyBytes)
}

func newToken() (string, error) {
	var value [tokenBytes]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func validToken(token string) bool {
	if len(token) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	return err == nil && len(raw) == tokenBytes
}

func tokenHash(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}

func rateKey(scope, value string) string {
	hash := sha256.Sum256([]byte(value))
	return scope + ":" + hex.EncodeToString(hash[:])
}
