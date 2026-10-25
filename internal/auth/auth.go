package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Unambiguous alphabet omitting 0, O, 1, I, etc.
const claimAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// GenerateDeviceSecret produces a 32-byte cryptographic random secret.
// Returns raw secret (hex-encoded string) and its SHA-256 hash bytes.
func GenerateDeviceSecret() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("failed to generate random secret: %w", err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(b), h[:], nil
}

// VerifyDeviceSecret validates whether rawSecret matches storedHash in constant time.
func VerifyDeviceSecret(rawSecretHex string, storedHash []byte) bool {
	raw, err := hex.DecodeString(rawSecretHex)
	if err != nil {
		return false
	}
	h := sha256.Sum256(raw)
	return subtle.ConstantTimeCompare(h[:], storedHash) == 1
}

// GenerateClaimCode generates a 10-character unambiguous claim code.
func GenerateClaimCode() (string, error) {
	code := make([]byte, 10)
	alphabetLen := big.NewInt(int64(len(claimAlphabet)))
	for i := 0; i < 10; i++ {
		idx, err := rand.Int(rand.Reader, alphabetLen)
		if err != nil {
			return "", fmt.Errorf("failed generating claim code: %w", err)
		}
		code[i] = claimAlphabet[idx.Int64()]
	}
	return string(code), nil
}

// Claims defines standard JWT claims for translator tokens.
type Claims struct {
	DeviceID string `json:"sub"`
	Role     string `json:"role"` // "device" or "owner"
	jwt.RegisteredClaims
}

// TokenManager handles issuing and verifying EdDSA signed JWTs.
type TokenManager struct {
	primaryKID string
	primaryKey ed25519.PrivateKey
	pubKeys    map[string]ed25519.PublicKey
	mu         sync.RWMutex
}

// NewTokenManager loads primary and optional rotation Ed25519 private keys from PEM files.
func NewTokenManager(primaryKeyPath, rotationKeyPath string) (*TokenManager, error) {
	priv, err := loadPrivateKey(primaryKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed loading primary jwt key from %s: %w", primaryKeyPath, err)
	}

	tm := &TokenManager{
		primaryKID: "key-1",
		primaryKey: priv,
		pubKeys: map[string]ed25519.PublicKey{
			"key-1": priv.Public().(ed25519.PublicKey),
		},
	}

	if rotationKeyPath != "" {
		rotPriv, err := loadPrivateKey(rotationKeyPath)
		if err == nil {
			tm.pubKeys["key-2"] = rotPriv.Public().(ed25519.PublicKey)
		}
	}

	return tm, nil
}

// NewTokenManagerFromKeys creates a TokenManager directly from Ed25519 private keys (useful for testing).
func NewTokenManagerFromKeys(primaryKID string, primaryKey ed25519.PrivateKey, secondaryKID string, secondaryKey ed25519.PrivateKey) *TokenManager {
	pubKeys := map[string]ed25519.PublicKey{
		primaryKID: primaryKey.Public().(ed25519.PublicKey),
	}
	if secondaryKey != nil {
		pubKeys[secondaryKID] = secondaryKey.Public().(ed25519.PublicKey)
	}
	return &TokenManager{
		primaryKID: primaryKID,
		primaryKey: primaryKey,
		pubKeys:    pubKeys,
	}
}

func loadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		// Try raw seed or private key
		if len(data) == ed25519.PrivateKeySize {
			return ed25519.PrivateKey(data), nil
		}
		if len(data) == ed25519.SeedSize {
			return ed25519.NewKeyFromSeed(data), nil
		}
		return nil, errors.New("failed to parse PEM block")
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err == nil {
		if k, ok := parsed.(ed25519.PrivateKey); ok {
			return k, nil
		}
	}

	// Try 64-byte raw in PEM block
	if len(block.Bytes) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(block.Bytes), nil
	}
	if len(block.Bytes) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(block.Bytes), nil
	}

	return nil, errors.New("unsupported Ed25519 private key format")
}

// IssueDeviceStreamToken generates a 15-minute stream token for a device.
func (tm *TokenManager) IssueDeviceStreamToken(deviceID uuid.UUID) (string, time.Time, error) {
	exp := time.Now().Add(15 * time.Minute)
	claims := Claims{
		DeviceID: deviceID.String(),
		Role:     "device",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   deviceID.String(),
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = tm.primaryKID

	signed, err := token.SignedString(tm.primaryKey)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

// IssueOwnerToken generates a 30-day token for companion device management.
func (tm *TokenManager) IssueOwnerToken(deviceID uuid.UUID) (string, time.Time, error) {
	exp := time.Now().Add(30 * 24 * time.Hour)
	claims := Claims{
		DeviceID: deviceID.String(),
		Role:     "owner",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   deviceID.String(),
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = tm.primaryKID

	signed, err := token.SignedString(tm.primaryKey)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

// ValidateToken parses and verifies an EdDSA token, returning Claims.
func (tm *TokenManager) ValidateToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if token.Method.Alg() != jwt.SigningMethodEdDSA.Alg() {
			return nil, fmt.Errorf("unexpected signing algorithm: %v", token.Header["alg"])
		}
		kid, _ := token.Header["kid"].(string)
		tm.mu.RLock()
		defer tm.mu.RUnlock()
		if kid != "" {
			if key, ok := tm.pubKeys[kid]; ok {
				return key, nil
			}
		}
		// Fallback to primary if kid missing or only 1 key
		if key, ok := tm.pubKeys[tm.primaryKID]; ok {
			return key, nil
		}
		return nil, errors.New("public key not found")
	})

	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}

	return claims, nil
}
