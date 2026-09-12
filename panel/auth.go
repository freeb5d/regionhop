package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type sessionStore struct {
	mu       sync.Mutex
	secret   []byte
	failures map[string]*loginAttempts
}

type loginAttempts struct {
	count      int
	lockedTill time.Time
}

func newSessionStore(secret []byte) *sessionStore {
	return &sessionStore{secret: secret, failures: map[string]*loginAttempts{}}
}

const sessionCookieName = "psi_session"
const sessionTTL = 12 * time.Hour

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *sessionStore) sign(value string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *sessionStore) issue(w http.ResponseWriter, r *http.Request) {
	exp := time.Now().Add(sessionTTL).Unix()
	value := formatSessionValue(exp)
	sig := s.sign(value)
	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    value + "." + sig,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		Expires:  time.Unix(exp, 0),
	}
	http.SetCookie(w, cookie)
}

func (s *sessionStore) valid(r *http.Request) bool {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	value, sig := parts[0], parts[1]
	expected := s.sign(value)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return false
	}
	exp, ok := parseSessionValue(value)
	if !ok || time.Now().Unix() > exp {
		return false
	}
	return true
}

func (s *sessionStore) clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// simple brute-force throttle keyed by remote IP: 5 attempts then 5 min lockout.
func (s *sessionStore) allowAttempt(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.failures[ip]
	if !ok {
		return true
	}
	if a.count >= 5 && time.Now().Before(a.lockedTill) {
		return false
	}
	return true
}

func (s *sessionStore) recordFailure(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.failures[ip]
	if !ok {
		a = &loginAttempts{}
		s.failures[ip] = a
	}
	a.count++
	if a.count >= 5 {
		a.lockedTill = time.Now().Add(5 * time.Minute)
	}
}

func (s *sessionStore) recordSuccess(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, ip)
}

func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func checkPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// clientIP deliberately does NOT trust X-Forwarded-For: the panel listens
// directly on the network (no reverse proxy in front of it by default), so
// that header is entirely attacker-controlled. Trusting it let anyone
// bypass the login lockout completely by sending a different spoofed value
// on every attempt — r.RemoteAddr is the one thing a client can't fake.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func formatSessionValue(exp int64) string {
	return time.Unix(exp, 0).UTC().Format(time.RFC3339)
}

func parseSessionValue(v string) (int64, bool) {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return 0, false
	}
	return t.Unix(), true
}
