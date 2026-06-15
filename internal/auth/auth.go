package auth

import (
	"context"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"tailscale.com/client/local"
)

// identityKey is the key under which the resolved Identity is stored in the
// Fiber request locals.
const identityKey = "identity"

// Identity represents the authenticated user behind a request.
type Identity struct {
	ID    string `json:"id"`    // stable user identifier (Tailscale UserID, or "local")
	Email string `json:"email"` // login name, e.g. "alice@example.com"
	Name  string `json:"name"`  // display name, e.g. "Alice Smith"
	Local bool   `json:"local"` // true when resolved via the local (non-tailnet) fallback
}

// LocalIdentity is assigned to requests that cannot be attributed to a tailnet
// user, e.g. connections on the local debug listener (localhost:8080) or when
// the node is not connected to a tailnet.
var LocalIdentity = Identity{
	ID:    "local",
	Email: "local@fastnas",
	Name:  "Local User",
	Local: true,
}

// New returns Fiber middleware that resolves the caller's Tailscale identity
// via WhoIs and stores it in the request locals. Requests that cannot be
// attributed to a tailnet user fall back to LocalIdentity, so the app remains
// usable on the local listener and without a tailnet.
func New(lc *local.Client) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Locals(identityKey, resolve(lc, c))
		return c.Next()
	}
}

// resolve maps the request's remote address to an Identity via Tailscale WhoIs.
func resolve(lc *local.Client, c *fiber.Ctx) Identity {
	if lc == nil {
		return LocalIdentity
	}

	remoteAddr := c.Context().RemoteAddr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	who, err := lc.WhoIs(ctx, remoteAddr)
	if err != nil || who == nil || who.UserProfile == nil {
		// Not a tailnet connection (e.g. localhost) or WhoIs unavailable.
		return LocalIdentity
	}

	up := who.UserProfile
	// Tagged devices have no real user (ID 0); treat them as the local fallback.
	if up.ID == 0 {
		return LocalIdentity
	}

	return Identity{
		ID:    strconv.FormatInt(int64(up.ID), 10),
		Email: up.LoginName,
		Name:  up.DisplayName,
	}
}

// Get returns the resolved Identity for the request, falling back to
// LocalIdentity if the middleware did not run.
func Get(c *fiber.Ctx) Identity {
	if v, ok := c.Locals(identityKey).(Identity); ok {
		return v
	}
	return LocalIdentity
}
