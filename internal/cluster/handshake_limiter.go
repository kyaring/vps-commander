package cluster

import (
	"sync"
	"time"
)

const (
	handshakeGlobalRate  = 50.0
	handshakeGlobalBurst = 200.0
	handshakeIPRate      = 5.0
	handshakeIPBurst     = 20.0
	handshakeBucketTTL   = 10 * time.Minute
	handshakeMaxBuckets  = 4096
)

type handshakeBucket struct {
	tokens   float64
	lastFill time.Time
	lastSeen time.Time
}

type handshakeLimiter struct {
	mu      sync.Mutex
	global  handshakeBucket
	clients map[string]*handshakeBucket
	checks  uint64
}

func newHandshakeLimiter(now time.Time) *handshakeLimiter {
	return &handshakeLimiter{
		global:  handshakeBucket{tokens: handshakeGlobalBurst, lastFill: now, lastSeen: now},
		clients: make(map[string]*handshakeBucket),
	}
}

func refillHandshakeBucket(b *handshakeBucket, now time.Time, rate, burst float64) {
	if now.After(b.lastFill) {
		b.tokens += now.Sub(b.lastFill).Seconds() * rate
		if b.tokens > burst {
			b.tokens = burst
		}
		b.lastFill = now
	}
	b.lastSeen = now
}

func (l *handshakeLimiter) Allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	refillHandshakeBucket(&l.global, now, handshakeGlobalRate, handshakeGlobalBurst)
	if l.global.tokens < 1 {
		return false
	}
	b := l.clients[ip]
	if b == nil {
		if len(l.clients) >= handshakeMaxBuckets || l.checks%256 == 0 {
			for key, candidate := range l.clients {
				if now.Sub(candidate.lastSeen) > handshakeBucketTTL {
					delete(l.clients, key)
				}
			}
		}
		if len(l.clients) >= handshakeMaxBuckets {
			// Keep memory bounded without permanently locking out new clients after churn.
			var oldestKey string
			var oldest time.Time
			for key, candidate := range l.clients {
				if oldestKey == "" || candidate.lastSeen.Before(oldest) {
					oldestKey, oldest = key, candidate.lastSeen
				}
			}
			if oldestKey != "" {
				delete(l.clients, oldestKey)
			}
		}
		b = &handshakeBucket{tokens: handshakeIPBurst, lastFill: now, lastSeen: now}
		l.clients[ip] = b
	}
	refillHandshakeBucket(b, now, handshakeIPRate, handshakeIPBurst)
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	l.global.tokens--
	l.checks++
	return true
}
