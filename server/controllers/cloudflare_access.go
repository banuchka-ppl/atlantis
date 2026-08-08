// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	cloudflareAccessJWTHeader   = "Cf-Access-Jwt-Assertion"
	cloudflareAccessHostSuffix  = ".cloudflareaccess.com"
	cloudflareAssertionMaxBytes = 16 * 1024
	cloudflareJWKSMaxBytes      = 64 * 1024
	cloudflareKeyCacheTTL       = time.Hour
	cloudflareMinRefreshPeriod  = time.Minute
)

var (
	cloudflareTeamNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	errInvalidAccessAssertion = errors.New("invalid Cloudflare Access assertion")
)

type cloudflareAccessClaims struct {
	jwt.RegisteredClaims
	Email string `json:"email"`
}

type cloudflareJWKSet struct {
	Keys []cloudflareJWK `json:"keys"`
}

type cloudflareJWK struct {
	Algorithm string `json:"alg"`
	Exponent  string `json:"e"`
	KeyID     string `json:"kid"`
	KeyType   string `json:"kty"`
	Modulus   string `json:"n"`
	Use       string `json:"use"`
}

// CloudflareAccessAuthenticator verifies Access assertions at the origin.
type CloudflareAccessAuthenticator struct {
	audience string
	issuer   string
	jwksURL  string
	client   *http.Client

	refreshMutex sync.Mutex
	keysMutex    sync.RWMutex
	keys         map[string]*rsa.PublicKey
	expiresAt    time.Time
	lastAttempt  time.Time
}

// NewCloudflareAccessAuthenticator creates a fail-closed origin verifier.
func NewCloudflareAccessAuthenticator(teamDomain, audience string, client *http.Client) (*CloudflareAccessAuthenticator, error) {
	team, err := normalizeCloudflareTeamDomain(teamDomain)
	if err != nil {
		return nil, err
	}
	audience = strings.TrimSpace(audience)
	if audience == "" || len(audience) > 256 || strings.ContainsAny(audience, " \t\r\n") {
		return nil, errors.New("Cloudflare Access audience must be a non-empty token")
	}
	if client == nil {
		expectedHost := team + cloudflareAccessHostSuffix
		client = &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(request *http.Request, _ []*http.Request) error {
				if request.URL.Scheme != "https" || request.URL.Hostname() != expectedHost {
					return errors.New("Cloudflare Access JWKS redirect left the configured issuer")
				}
				return nil
			},
		}
	}
	issuer := "https://" + team + cloudflareAccessHostSuffix
	return &CloudflareAccessAuthenticator{
		audience: audience,
		issuer:   issuer,
		jwksURL:  issuer + "/cdn-cgi/access/certs",
		client:   client,
		keys:     make(map[string]*rsa.PublicKey),
	}, nil
}

func normalizeCloudflareTeamDomain(teamDomain string) (string, error) {
	team := strings.ToLower(strings.TrimSpace(teamDomain))
	team = strings.TrimSuffix(team, cloudflareAccessHostSuffix)
	if !cloudflareTeamNamePattern.MatchString(team) {
		return "", errors.New("Cloudflare Access team domain must be a single cloudflareaccess.com team label")
	}
	return team, nil
}

// Authenticate verifies the signed assertion, exact application, and human identity.
func (a *CloudflareAccessAuthenticator) Authenticate(request *http.Request) error {
	assertion := request.Header.Get(cloudflareAccessJWTHeader)
	if assertion == "" || len(assertion) > cloudflareAssertionMaxBytes {
		return errInvalidAccessAssertion
	}
	claims := &cloudflareAccessClaims{}
	token, err := jwt.ParseWithClaims(
		assertion,
		claims,
		func(token *jwt.Token) (any, error) {
			keyID, ok := token.Header["kid"].(string)
			if !ok || keyID == "" || len(keyID) > 256 {
				return nil, errInvalidAccessAssertion
			}
			return a.key(request.Context(), keyID)
		},
		jwt.WithAudience(a.audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuer(a.issuer),
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
	)
	if err != nil || !token.Valid || strings.TrimSpace(claims.Email) == "" {
		return errInvalidAccessAssertion
	}
	return nil
}

func (a *CloudflareAccessAuthenticator) key(ctx context.Context, keyID string) (*rsa.PublicKey, error) {
	now := time.Now()
	key, expiresAt, lastAttempt := a.keySnapshot(keyID)
	if key != nil && now.Before(expiresAt) {
		return key, nil
	}

	a.refreshMutex.Lock()
	defer a.refreshMutex.Unlock()
	key, expiresAt, lastAttempt = a.keySnapshot(keyID)
	now = time.Now()
	if key != nil && now.Before(expiresAt) {
		return key, nil
	}
	if !lastAttempt.IsZero() && now.Sub(lastAttempt) < cloudflareMinRefreshPeriod {
		if key != nil {
			return key, nil
		}
		return nil, errInvalidAccessAssertion
	}
	a.keysMutex.Lock()
	a.lastAttempt = now
	a.keysMutex.Unlock()

	keys, err := a.fetchKeys(ctx)
	if err != nil {
		if key != nil {
			return key, nil
		}
		return nil, errInvalidAccessAssertion
	}
	a.keysMutex.Lock()
	a.keys = keys
	a.expiresAt = now.Add(cloudflareKeyCacheTTL)
	key = a.keys[keyID]
	a.keysMutex.Unlock()
	if key == nil {
		return nil, errInvalidAccessAssertion
	}
	return key, nil
}

func (a *CloudflareAccessAuthenticator) keySnapshot(keyID string) (*rsa.PublicKey, time.Time, time.Time) {
	a.keysMutex.RLock()
	defer a.keysMutex.RUnlock()
	return a.keys[keyID], a.expiresAt, a.lastAttempt
}

func (a *CloudflareAccessAuthenticator) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.jwksURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := a.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Cloudflare Access JWKS returned status %d", response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, cloudflareJWKSMaxBytes+1))
	if err != nil || len(content) > cloudflareJWKSMaxBytes {
		return nil, errors.New("Cloudflare Access JWKS exceeds its response bound")
	}
	var set cloudflareJWKSet
	if err := json.Unmarshal(content, &set); err != nil {
		return nil, err
	}
	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, key := range set.Keys {
		publicKey, err := parseCloudflareJWK(key)
		if err != nil {
			continue
		}
		keys[key.KeyID] = publicKey
	}
	if len(keys) == 0 {
		return nil, errors.New("Cloudflare Access JWKS contains no usable signing keys")
	}
	return keys, nil
}

func parseCloudflareJWK(key cloudflareJWK) (*rsa.PublicKey, error) {
	if key.KeyID == "" || len(key.KeyID) > 256 || key.KeyType != "RSA" || key.Algorithm != "RS256" || key.Use != "sig" {
		return nil, errInvalidAccessAssertion
	}
	modulus, err := base64.RawURLEncoding.DecodeString(key.Modulus)
	if err != nil {
		return nil, err
	}
	exponentBytes, err := base64.RawURLEncoding.DecodeString(key.Exponent)
	if err != nil || len(exponentBytes) == 0 || len(exponentBytes) > 4 {
		return nil, errInvalidAccessAssertion
	}
	exponent := new(big.Int).SetBytes(exponentBytes)
	if !exponent.IsInt64() || exponent.Int64() < 3 || exponent.Int64() > math.MaxInt32 || exponent.Int64()%2 == 0 {
		return nil, errInvalidAccessAssertion
	}
	publicKey := &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(exponent.Int64())}
	if publicKey.N.BitLen() < 2048 {
		return nil, errInvalidAccessAssertion
	}
	return publicKey, nil
}
