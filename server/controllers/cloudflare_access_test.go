// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	. "github.com/runatlantis/atlantis/testing"
)

const (
	cloudflareTeamDomain = "perplexity-ai"
	cloudflareAudience   = "eba3eb6aee1ac80b1ec1c821625edd733bcf801a40651b2ecee09ed8b6b96801"
	cloudflareKeyID      = "test-key"
)

func TestCloudflareAccessAuthenticatorAuthenticatesExactHumanAudience(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	Ok(t, err)
	jwksServer := newJWKSServer(t, &privateKey.PublicKey)
	authenticator, err := NewCloudflareAccessAuthenticator(cloudflareTeamDomain, cloudflareAudience, jwksServer.Client())
	Ok(t, err)
	authenticator.jwksURL = jwksServer.URL

	request := httptest.NewRequest(http.MethodGet, "/jobs/example/diagnostic", nil)
	request.Header.Set(cloudflareAccessJWTHeader, signedAccessToken(t, privateKey, cloudflareAccessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{cloudflareAudience},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			Issuer:    "https://perplexity-ai.cloudflareaccess.com",
		},
		Email: "operator@perplexity.ai",
	}))

	Ok(t, authenticator.Authenticate(request))
}

func TestCloudflareAccessAuthenticatorRejectsUntrustedAssertions(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	Ok(t, err)
	jwksServer := newJWKSServer(t, &privateKey.PublicKey)
	authenticator, err := NewCloudflareAccessAuthenticator(cloudflareTeamDomain, cloudflareAudience, jwksServer.Client())
	Ok(t, err)
	authenticator.jwksURL = jwksServer.URL

	tests := []struct {
		name   string
		claims cloudflareAccessClaims
	}{
		{
			name: "wrong audience",
			claims: cloudflareAccessClaims{
				RegisteredClaims: jwt.RegisteredClaims{
					Audience:  jwt.ClaimStrings{"another-application"},
					ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
					Issuer:    "https://perplexity-ai.cloudflareaccess.com",
				},
				Email: "operator@perplexity.ai",
			},
		},
		{
			name: "expired",
			claims: cloudflareAccessClaims{
				RegisteredClaims: jwt.RegisteredClaims{
					Audience:  jwt.ClaimStrings{cloudflareAudience},
					ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
					Issuer:    "https://perplexity-ai.cloudflareaccess.com",
				},
				Email: "operator@perplexity.ai",
			},
		},
		{
			name: "service identity without email",
			claims: cloudflareAccessClaims{
				RegisteredClaims: jwt.RegisteredClaims{
					Audience:  jwt.ClaimStrings{cloudflareAudience},
					ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
					Issuer:    "https://perplexity-ai.cloudflareaccess.com",
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/jobs/example/diagnostic", nil)
			request.Header.Set(cloudflareAccessJWTHeader, signedAccessToken(t, privateKey, test.claims))

			Assert(t, authenticator.Authenticate(request) != nil, "untrusted assertion was accepted")
		})
	}
}

func TestNewCloudflareAccessAuthenticatorRejectsUnsafeTeamDomain(t *testing.T) {
	_, err := NewCloudflareAccessAuthenticator("attacker.example", cloudflareAudience, nil)
	Assert(t, err != nil, "unsafe JWKS domain was accepted")
}

func newJWKSServer(t *testing.T, publicKey *rsa.PublicKey) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		exponent := big.NewInt(int64(publicKey.E)).Bytes()
		Ok(t, json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"alg": "RS256",
				"e":   base64.RawURLEncoding.EncodeToString(exponent),
				"kid": cloudflareKeyID,
				"kty": "RSA",
				"n":   base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
				"use": "sig",
			}},
		}))
	}))
}

func signedAccessToken(t *testing.T, privateKey *rsa.PrivateKey, claims cloudflareAccessClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = cloudflareKeyID
	signed, err := token.SignedString(privateKey)
	Ok(t, err)
	return signed
}
