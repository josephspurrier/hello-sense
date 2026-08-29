// Package push sends Apple push notifications.
//
// It talks to APNS directly over HTTP/2 with a token-based (.p8) key, rather
// than going Kinesis -> SNS -> APNS as the reference did. That chain existed to
// fan out to a fleet from a service that had no long-lived process; orb already
// has one, and there is one phone. Going direct removes a stream, a queue, an
// AWS service, and the credential rotation that comes with a certificate.
//
// No dependencies: net/http negotiates HTTP/2 over TLS by itself, and the JWT
// is small enough to sign with crypto/ecdsa.
package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Hosts. A development build's token is only valid against the sandbox, and a
// production build's only against production. Sending to the wrong one returns
// BadDeviceToken, which reads as "the token is wrong" when the token is fine
// and the host is not. This is the single most common way to lose an evening
// to APNS.
const (
	HostSandbox    = "https://api.sandbox.push.apple.com"
	HostProduction = "https://api.push.apple.com"
)

// tokenLifetime is how long a provider JWT is reused.
//
// Apple rejects a token older than one hour, and separately rejects a client
// that mints them too eagerly with TooManyProviderTokenUpdates. So this sits in
// the middle: comfortably inside the hour, comfortably outside the rate limit.
const tokenLifetime = 45 * time.Minute

// ErrUnregistered means the app is no longer installed, or the token has been
// replaced. The caller should delete the token rather than retry it.
var ErrUnregistered = errors.New("push: token is no longer registered")

// Client sends notifications for one app.
type Client struct {
	host   string
	topic  string // the app's bundle id
	teamID string
	keyID  string
	key    *ecdsa.PrivateKey
	http   *http.Client

	mu        sync.Mutex
	jwt       string
	jwtMinted time.Time
}

// Config is what the .p8 key and the developer account provide.
type Config struct {
	KeyPath string // path to AuthKey_XXXXXXXXXX.p8
	KeyID   string // the ten characters in the key's filename
	TeamID  string // the developer account's team id
	Topic   string // the app's bundle id, used as apns-topic
	Host    string // HostSandbox or HostProduction
}

// New loads the signing key and prepares a client.
func New(cfg Config) (*Client, error) {
	if cfg.KeyID == "" || cfg.TeamID == "" || cfg.Topic == "" {
		return nil, errors.New("push: key id, team id and topic are all required")
	}
	if cfg.Host == "" {
		cfg.Host = HostSandbox
	}

	pemBytes, err := os.ReadFile(cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("push: read key: %w", err)
	}
	key, err := parseP8(pemBytes)
	if err != nil {
		return nil, err
	}

	return &Client{
		host:   strings.TrimSuffix(cfg.Host, "/"),
		topic:  cfg.Topic,
		teamID: cfg.TeamID,
		keyID:  cfg.KeyID,
		key:    key,
		// A short timeout on purpose: this runs on a path that must not wedge,
		// and a push that is late is worthless anyway.
		http: &http.Client{Timeout: 20 * time.Second},
	}, nil
}

// parseP8 reads Apple's PKCS#8 EC private key.
func parseP8(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("push: key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("push: parse key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("push: key is %T, want an ECDSA key", parsed)
	}
	return key, nil
}

// providerToken returns a signed JWT, minting a new one only when the old one
// is stale.
func (c *Client) providerToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.jwt != "" && time.Since(c.jwtMinted) < tokenLifetime {
		return c.jwt, nil
	}

	now := time.Now()
	header := map[string]string{"alg": "ES256", "kid": c.keyID}
	claims := map[string]any{"iss": c.teamID, "iat": now.Unix()}

	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := b64(hb) + "." + b64(cb)

	// ES256 signatures are the fixed-width concatenation of R and S, NOT the
	// ASN.1 DER that ecdsa.SignASN1 produces. Sending DER gets a 403
	// InvalidProviderToken that looks exactly like a wrong key id.
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, c.key, sum[:])
	if err != nil {
		return "", fmt.Errorf("push: sign: %w", err)
	}
	sig := append(pad32(r), pad32(s)...)

	c.jwt = signing + "." + b64(sig)
	c.jwtMinted = now
	return c.jwt, nil
}

// pad32 left-pads a signature half to the 32 bytes P-256 requires. A big.Int
// with a leading zero byte encodes short, and a 63-byte signature is rejected.
func pad32(n *big.Int) []byte {
	b := n.Bytes()
	if len(b) >= 32 {
		return b
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// Alert is a user-visible notification.
type Alert struct {
	Title string
	Body  string
}

// Send delivers one alert to one device token.
func (c *Client) Send(ctx context.Context, deviceToken string, a Alert) error {
	jwt, err := c.providerToken()
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert": map[string]string{"title": a.Title, "body": a.Body},
			"sound": "default",
		},
	})
	if err != nil {
		return err
	}

	url := c.host + "/3/device/" + deviceToken
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", c.topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("push: send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	// Apple explains itself in the body, and the reason string is the only
	// useful part of a failure. Without it every problem is "it did not work".
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)

	if body.Reason == "Unregistered" || body.Reason == "BadDeviceToken" {
		return fmt.Errorf("%w (%s)", ErrUnregistered, body.Reason)
	}
	return fmt.Errorf("push: apns %d: %s", resp.StatusCode, body.Reason)
}
