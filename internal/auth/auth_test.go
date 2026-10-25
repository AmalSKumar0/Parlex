package auth_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"parlex/internal/auth"
)

func TestDeviceSecret(t *testing.T) {
	secretHex, secretHash, err := auth.GenerateDeviceSecret()
	require.NoError(t, err)
	assert.Len(t, secretHex, 64) // 32 bytes in hex
	assert.Len(t, secretHash, 32)

	// Valid secret match
	assert.True(t, auth.VerifyDeviceSecret(secretHex, secretHash))

	// Invalid secret mismatch
	assert.False(t, auth.VerifyDeviceSecret("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff", secretHash))
	assert.False(t, auth.VerifyDeviceSecret("invalid-hex", secretHash))
}

func TestClaimCode(t *testing.T) {
	code, err := auth.GenerateClaimCode()
	require.NoError(t, err)
	assert.Len(t, code, 10)

	// Check unambiguous alphabet (no 0, O, 1, I)
	for _, c := range code {
		assert.False(t, strings.ContainsRune("0O1I", c), "claim code should not contain ambiguous chars")
	}
}

func TestTokenManager(t *testing.T) {
	_, priv1, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, priv2, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	tm := auth.NewTokenManagerFromKeys("key-1", priv1, "key-2", priv2)
	devID := uuid.New()

	t.Run("Issue and validate device stream token", func(t *testing.T) {
		token, exp, err := tm.IssueDeviceStreamToken(devID)
		require.NoError(t, err)
		assert.True(t, exp.After(time.Now()))

		claims, err := tm.ValidateToken(token)
		require.NoError(t, err)
		assert.Equal(t, devID.String(), claims.DeviceID)
		assert.Equal(t, "device", claims.Role)
	})

	t.Run("Issue and validate owner token", func(t *testing.T) {
		token, exp, err := tm.IssueOwnerToken(devID)
		require.NoError(t, err)
		assert.True(t, exp.After(time.Now().Add(29*24*time.Hour)))

		claims, err := tm.ValidateToken(token)
		require.NoError(t, err)
		assert.Equal(t, devID.String(), claims.DeviceID)
		assert.Equal(t, "owner", claims.Role)
	})

	t.Run("Validates token signed with rotation key", func(t *testing.T) {
		tmRotated := auth.NewTokenManagerFromKeys("key-2", priv2, "", nil)
		rotToken, _, err := tmRotated.IssueDeviceStreamToken(devID)
		require.NoError(t, err)

		// Primary TM should accept token signed by key-2 because it knows key-2
		claims, err := tm.ValidateToken(rotToken)
		require.NoError(t, err)
		assert.Equal(t, devID.String(), claims.DeviceID)
	})

	t.Run("Rejects token signed with unknown key", func(t *testing.T) {
		_, unknownPriv, _ := ed25519.GenerateKey(rand.Reader)
		tmUnknown := auth.NewTokenManagerFromKeys("key-unknown", unknownPriv, "", nil)
		badToken, _, err := tmUnknown.IssueDeviceStreamToken(devID)
		require.NoError(t, err)

		_, err = tm.ValidateToken(badToken)
		require.Error(t, err)
	})
}
