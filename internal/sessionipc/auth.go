package sessionipc

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
)

const (
	addressEnvironment = "SHELL_PICKER_ADDR"
	tokenEnvironment   = "SHELL_PICKER_TOKEN"
)

type Token [32]byte

func NewToken() (Token, error) {
	var token Token
	if _, err := rand.Read(token[:]); err != nil {
		return Token{}, errors.New("generate IPC credential")
	}
	return token, nil
}

func (token Token) String() string {
	return base64.RawURLEncoding.EncodeToString(token[:])
}

func parseToken(raw string) (Token, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(decoded) != len(Token{}) || base64.RawURLEncoding.EncodeToString(decoded) != raw {
		return Token{}, errors.New("invalid IPC credential")
	}
	var token Token
	copy(token[:], decoded)
	return token, nil
}

func (token Token) authorized(header string) bool {
	want := []byte("Bearer " + token.String())
	return subtle.ConstantTimeCompare([]byte(header), want) == 1
}
