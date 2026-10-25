package garmin

import (
	"fmt"
	"testing"
	"time"
)

func TestCacheBoundEvictsExpiredFirstThenOldest(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	c := newTokenCache(3)
	c.put("a", "ja", base.Add(time.Hour), base)                    // oldest, valid
	c.put("b", "jb", base.Add(time.Minute), base.Add(time.Second)) // will expire
	c.put("c", "jc", base.Add(time.Hour), base.Add(2*time.Second))
	now := base.Add(10 * time.Minute)
	c.put("d", "jd", now.Add(time.Hour), now)
	if _, ok := c.entries["b"]; ok {
		t.Fatal("expired entry should be evicted first")
	}
	if len(c.entries) != 3 {
		t.Fatalf("len = %d", len(c.entries))
	}
	c.put("e", "je", now.Add(time.Hour), now.Add(time.Second))
	if _, ok := c.entries["a"]; ok {
		t.Fatal("oldest should be evicted")
	}
	if len(c.entries) != 3 {
		t.Fatalf("len = %d", len(c.entries))
	}
}

func TestCacheDefaultBound(t *testing.T) {
	c := newTokenCache(maxCacheEntries)
	now := time.Unix(1_800_000_000, 0)
	for i := 0; i < maxCacheEntries+50; i++ {
		c.put(fmt.Sprint(i), "j", now.Add(time.Hour), now.Add(time.Duration(i)))
	}
	if len(c.entries) != maxCacheEntries {
		t.Fatalf("len = %d", len(c.entries))
	}
}
