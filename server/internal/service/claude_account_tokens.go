package service

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// loginTokenStore issues 5-min HMAC tokens for the WS login PTY handler.
// I5: tokens are single-use — Verify() marks the token used and rejects
// subsequent attempts even if signature/expiry valid. Used-map is GC'd
// once per minute to bound memory.
type loginTokenStore struct {
	secret []byte
	used   sync.Map // map[string]int64 (token sig → expiry unix; entry存在即 used)
}

func newLoginTokenStore() *loginTokenStore {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	s := &loginTokenStore{secret: b}
	go s.gcLoop()
	return s
}

// Issue returns a token: <accountID>.<expiryUnix>.<hmac>
func (s *loginTokenStore) Issue(accountID int64) (string, error) {
	exp := time.Now().Add(5 * time.Minute).Unix()
	body := fmt.Sprintf("%d.%d", accountID, exp)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(body))
	sig := hex.EncodeToString(mac.Sum(nil))
	return body + "." + sig, nil
}

// Verify validates signature + expiry, then marks the token used. Returns
// (accountID, nil) on first success; subsequent calls with the same token
// return ("token already used") even if signature is still valid.
func (s *loginTokenStore) Verify(tok string) (int64, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return 0, errors.New("malformed token")
	}
	body := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(body))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return 0, errors.New("bad signature")
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return 0, errors.New("token expired")
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, errors.New("malformed account id")
	}
	if _, loaded := s.used.LoadOrStore(parts[2], exp); loaded {
		return 0, errors.New("token already used")
	}
	return id, nil
}

func (s *loginTokenStore) gcLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		now := time.Now().Unix()
		s.used.Range(func(k, v any) bool {
			if exp, ok := v.(int64); ok && now > exp {
				s.used.Delete(k)
			}
			return true
		})
	}
}
