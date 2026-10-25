package garmin

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLive(t *testing.T) {
	if os.Getenv("GARMIN_LIVE") != "1" {
		t.Skip("GARMIN_LIVE != 1")
	}
	raw, err := os.ReadFile("../../test/api.key")
	if err != nil {
		t.Skip("test/api.key not available")
	}
	key := strings.TrimSpace(string(raw))
	c := NewClient("https://personal.connectapi.garmin.com", &http.Client{Timeout: 30 * time.Second})
	ctx := context.Background()
	if err := c.Validate(ctx, key); err != nil {
		t.Fatalf("validate: %v", err)
	}
	resp, err := c.Get(ctx, key, "/userprofile-service/userprofile/settings", nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.Status != 200 {
		t.Fatalf("status = %d", resp.Status)
	}
}
