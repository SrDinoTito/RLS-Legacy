// Package auth provides password hashing (bcrypt) and HS256 JWT helpers for
// the dashboard session cookies.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is the login hashing cost mandated by the spec.
const bcryptCost = 12

// HashPassword hashes a plaintext password with bcrypt cost 12.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}

// ComparePassword reports whether the password matches the stored hash.
func ComparePassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

// ErrInvalidToken is returned when a JWT fails validation (signature, method,
// expiry or malformed payload).
var ErrInvalidToken = errors.New("invalid token")

// Claims carried in every session JWT (HS256, TTL 24h).
const (
	ClaimSub  = "sub"  // user id
	ClaimRole = "role" // admin | viewer
)

// MintToken issues a signed HS256 JWT for the given user id and role.
func MintToken(secret []byte, userID int64, role string, ttl time.Duration, now time.Time) (string, error) {
	claims := jwt.MapClaims{
		"sub":  userID,
		"role": role,
		"iat":  now.Unix(),
		"exp":  now.Add(ttl).Unix(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

// ParseToken validates an HS256 JWT (only that method is accepted) and returns
// the user id and role from the claims.
func ParseToken(secret []byte, tokenStr string, now time.Time) (userID int64, role string, err error) {
	tok, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		return secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !tok.Valid {
		return 0, "", ErrInvalidToken
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return 0, "", ErrInvalidToken
	}
	sub, ok := claims[ClaimSub].(float64) // JSON numbers unmarshal as float64
	if !ok {
		return 0, "", ErrInvalidToken
	}
	r, ok := claims[ClaimRole].(string)
	if !ok {
		return 0, "", ErrInvalidToken
	}
	return int64(sub), r, nil
}
