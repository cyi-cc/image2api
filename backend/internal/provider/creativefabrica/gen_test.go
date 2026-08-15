package creativefabrica

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestBalance(t *testing.T) {
	cookie := os.Getenv("CF_COOKIE")
	if cookie == "" {
		t.Skip("CF_COOKIE not set")
	}
	c := NewClient("")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tok, uid, err := c.ExchangeToken(ctx, cookie)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	bal, err := c.FetchBalance(ctx, cookie)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	t.Logf("user=%s token=%d bal=%d", uid, len(tok), bal)
}
