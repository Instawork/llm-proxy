package circuit

import (
	"strings"
	"testing"
)

// TestNewRedisStore_URLOnly verifies that a redis:// URL alone is sufficient
// and the resulting client inherits host, DB, and password from the URL.
func TestNewRedisStore_URLOnly(t *testing.T) {
	s, err := NewRedisStore(Config{
		Enabled:  true,
		Backend:  "redis",
		RedisURL: "redis://:supersecret@cache.example.com:6379/3",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	opts := s.rdb.Options()
	if opts.Addr != "cache.example.com:6379" {
		t.Fatalf("want Addr=cache.example.com:6379, got %q", opts.Addr)
	}
	if opts.Password != "supersecret" {
		t.Fatalf("want Password=supersecret, got %q", opts.Password)
	}
	if opts.DB != 3 {
		t.Fatalf("want DB=3, got %d", opts.DB)
	}
}

// TestNewRedisStore_URLPlusDBOverlay confirms that an explicit RedisDB
// overrides the DB encoded in the URL, so operators can share Finch's
// cluster while pinning a dedicated circuit-breaker DB.
func TestNewRedisStore_URLPlusDBOverlay(t *testing.T) {
	s, err := NewRedisStore(Config{
		Enabled:    true,
		Backend:    "redis",
		RedisURL:   "redis://cache.example.com:6379/6", // Finch uses DB 6
		RedisDB:    5,                                  // circuit breaker pinned to DB 5
		RedisDBSet: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := s.rdb.Options().DB; got != 5 {
		t.Fatalf("explicit RedisDB must override URL DB; got %d", got)
	}
}

// TestNewRedisStore_URLPlusExplicitZeroDBOverlay confirms that an explicit
// RedisDB=0 can override a DB encoded in the URL.
func TestNewRedisStore_URLPlusExplicitZeroDBOverlay(t *testing.T) {
	s, err := NewRedisStore(Config{
		Enabled:    true,
		Backend:    "redis",
		RedisURL:   "redis://cache.example.com:6379/6",
		RedisDB:    0,
		RedisDBSet: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := s.rdb.Options().DB; got != 0 {
		t.Fatalf("explicit RedisDB=0 must override URL DB; got %d", got)
	}
}

// TestNewRedisStore_URLPlusPasswordOverlay confirms the same for password.
func TestNewRedisStore_URLPlusPasswordOverlay(t *testing.T) {
	s, err := NewRedisStore(Config{
		Enabled:       true,
		Backend:       "redis",
		RedisURL:      "redis://:urlpw@cache.example.com:6379/0",
		RedisPassword: "overridepw",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := s.rdb.Options().Password; got != "overridepw" {
		t.Fatalf("explicit RedisPassword must override URL password; got %q", got)
	}
}

// TestNewRedisStore_EmptyPasswordLeavesURLAlone documents that an empty
// RedisPassword is treated as "don't override" rather than "force blank".
// This is the behaviour that lets operators pass a URL-with-credentials
// while leaving the individual field unset.
func TestNewRedisStore_EmptyPasswordLeavesURLAlone(t *testing.T) {
	s, err := NewRedisStore(Config{
		Enabled:       true,
		Backend:       "redis",
		RedisURL:      "redis://:fromurl@cache.example.com:6379/0",
		RedisPassword: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := s.rdb.Options().Password; got != "fromurl" {
		t.Fatalf("empty RedisPassword must NOT clear URL password; got %q", got)
	}
}

// TestNewRedisStore_AddressFallback confirms that when no URL is provided
// we still accept the legacy Address/Password/DB triple.
func TestNewRedisStore_AddressFallback(t *testing.T) {
	s, err := NewRedisStore(Config{
		Enabled:       true,
		Backend:       "redis",
		RedisAddress:  "cache.example.com:6379",
		RedisPassword: "pw",
		RedisDB:       2,
		RedisDBSet:    true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	opts := s.rdb.Options()
	if opts.Addr != "cache.example.com:6379" || opts.Password != "pw" || opts.DB != 2 {
		t.Fatalf("address-only fallback broken: %+v", opts)
	}
}

// TestNewRedisStore_NeitherURLNorAddress is the "misconfigured" path —
// must surface a clear error rather than build a zero-value client.
func TestNewRedisStore_NeitherURLNorAddress(t *testing.T) {
	_, err := NewRedisStore(Config{Enabled: true, Backend: "redis"})
	if err == nil {
		t.Fatalf("expected error when neither URL nor Address is set")
	}
	if !strings.Contains(err.Error(), "address or url") {
		t.Fatalf("error message should mention missing URL/address; got: %v", err)
	}
}

// TestNewRedisStore_MalformedURL surfaces parse errors rather than
// silently producing a broken client.
func TestNewRedisStore_MalformedURL(t *testing.T) {
	_, err := NewRedisStore(Config{
		Enabled:  true,
		Backend:  "redis",
		RedisURL: "not-a-url://garbage",
	})
	if err == nil {
		t.Fatalf("expected parse error on malformed URL")
	}
	// We specifically want the factory error wrapper (parse redis_url),
	// not some unrelated validation error — asserting on the wrapper
	// prevents accidental changes where a future refactor would bypass
	// redis.ParseURL and hide the underlying problem.
	if !strings.Contains(err.Error(), "parse redis_url") {
		t.Fatalf("error message should wrap the parse failure; got: %v", err)
	}
}
