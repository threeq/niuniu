package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateAndValidateAccessToken(t *testing.T) {
	secret := "test-secret-key"
	claims := &Claims{UserID: 1, Username: "admin", Role: "admin"}

	token, err := GenerateAccessToken(claims, secret, 15*time.Minute)
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	parsed, err := ValidateToken(token, secret)
	require.NoError(t, err)
	assert.Equal(t, int64(1), parsed.UserID)
	assert.Equal(t, "admin", parsed.Username)
	assert.Equal(t, "admin", parsed.Role)
}

func TestValidateToken_Expired(t *testing.T) {
	secret := "test-secret-key"
	claims := &Claims{UserID: 1, Username: "test", Role: "member"}
	token, err := GenerateAccessToken(claims, secret, -1*time.Second)
	require.NoError(t, err)
	_, err = ValidateToken(token, secret)
	assert.Error(t, err)
}

func TestValidateToken_WrongSecret(t *testing.T) {
	token, _ := GenerateAccessToken(&Claims{UserID: 1, Username: "test", Role: "member"}, "secret1", 15*time.Minute)
	_, err := ValidateToken(token, "secret2")
	assert.Error(t, err)
}

func TestGenerateRefreshToken(t *testing.T) {
	token, err := GenerateRefreshToken()
	require.NoError(t, err)
	assert.Len(t, token, 64)
}
