package circuit

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
)

func newPingRedisServer(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake redis: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		for {
			cmd, err := readRedisCommand(reader)
			if err != nil || len(cmd) == 0 {
				return
			}
			switch strings.ToUpper(cmd[0]) {
			case "HELLO":
				_, _ = conn.Write([]byte("%1\r\n+server\r\n+redis\r\n"))
			case "PING":
				_, _ = conn.Write([]byte("+PONG\r\n"))
				return
			default:
				_, _ = conn.Write([]byte("+OK\r\n"))
			}
		}
	}()

	return ln.Addr().String()
}

func readRedisCommand(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("unexpected redis command prefix: %q", line)
	}
	count, err := strconv.Atoi(strings.TrimPrefix(line, "*"))
	if err != nil {
		return nil, err
	}

	parts := make([]string, 0, count)
	for i := 0; i < count; i++ {
		lenLine, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		lenLine = strings.TrimSpace(lenLine)
		if !strings.HasPrefix(lenLine, "$") {
			return nil, fmt.Errorf("unexpected redis bulk prefix: %q", lenLine)
		}
		n, err := strconv.Atoi(strings.TrimPrefix(lenLine, "$"))
		if err != nil {
			return nil, err
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return nil, err
		}
		parts = append(parts, string(buf[:n]))
	}
	return parts, nil
}

// TestNewRedisStore_URLOnly verifies that a redis:// URL alone is sufficient
// and the resulting client inherits host, DB, and password from the URL.
func TestNewRedisStore_URLOnly(t *testing.T) {
	addr := newPingRedisServer(t)
	s, err := NewRedisStore(Config{
		Enabled:  true,
		Backend:  "redis",
		RedisURL: "redis://:supersecret@" + addr + "/3",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	opts := s.rdb.Options()
	if opts.Addr != addr {
		t.Fatalf("want Addr=%s, got %q", addr, opts.Addr)
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
	addr := newPingRedisServer(t)
	s, err := NewRedisStore(Config{
		Enabled:    true,
		Backend:    "redis",
		RedisURL:   "redis://" + addr + "/6", // Finch uses DB 6
		RedisDB:    5,                        // circuit breaker pinned to DB 5
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
	addr := newPingRedisServer(t)
	s, err := NewRedisStore(Config{
		Enabled:    true,
		Backend:    "redis",
		RedisURL:   "redis://" + addr + "/6",
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
	addr := newPingRedisServer(t)
	s, err := NewRedisStore(Config{
		Enabled:       true,
		Backend:       "redis",
		RedisURL:      "redis://:urlpw@" + addr + "/0",
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
	addr := newPingRedisServer(t)
	s, err := NewRedisStore(Config{
		Enabled:       true,
		Backend:       "redis",
		RedisURL:      "redis://:fromurl@" + addr + "/0",
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
	addr := newPingRedisServer(t)
	s, err := NewRedisStore(Config{
		Enabled:       true,
		Backend:       "redis",
		RedisAddress:  addr,
		RedisPassword: "pw",
		RedisDB:       2,
		RedisDBSet:    true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	opts := s.rdb.Options()
	if opts.Addr != addr || opts.Password != "pw" || opts.DB != 2 {
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
