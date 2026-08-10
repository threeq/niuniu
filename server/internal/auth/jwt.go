package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

func GenerateAccessToken(claims *Claims, secret string, expiry time.Duration) (string, error) {
	now := time.Now()
	claims.RegisteredClaims = jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(now.Add(expiry)),
		IssuedAt:  jwt.NewNumericDate(now),
		Issuer:    "niuniu",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func ValidateToken(tokenString string, secret string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}
	return claims, nil
}

func GenerateRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate refresh token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// MFAStateClaims is a short-lived JWT used between /login (password OK, MFA
// needed) and /mfa/verify (code validated). Purpose="mfa" prevents it from
// being accepted as a regular access token.
type MFAStateClaims struct {
	UserID   int64  `json:"user_id"`
	Purpose  string `json:"purpose"`
	Nonce    string `json:"nonce"`
	jwt.RegisteredClaims
}

func GenerateMFAStateToken(userID int64, secret string, expiry time.Duration) (string, error) {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	now := time.Now()
	claims := MFAStateClaims{
		UserID:  userID,
		Purpose: "mfa",
		Nonce:   hex.EncodeToString(nonce),
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(expiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    "niuniu",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func ValidateMFAStateToken(tokenString string, secret string) (*MFAStateClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &MFAStateClaims{}, func(token *jwt.Token) (any, error) {
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*MFAStateClaims)
	if !ok || !token.Valid || claims.Purpose != "mfa" {
		return nil, fmt.Errorf("invalid mfa state token")
	}
	return claims, nil
}
