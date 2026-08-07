package service

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"backend/internal/model"
	"backend/internal/repo"
	"backend/internal/storage"
)

// MaintenanceService runs the periodic self-healing sweep that the Python
// original did via a 60s daemon thread plus read-time lazy cleanup. Without it
// the Go token pool only ever loses capacity: tokens never re-activate after a
// quota reset, cookies never auto-renew, stale pending events permanently block
// a user's generation gate, and old media/logs accumulate unbounded.
type MaintenanceService struct {
	tokens          *repo.TokenRepository
	tokenSvc        *TokenService
	events          *repo.EventRepository
	users           *repo.UserRepository
	refresh         *RefreshProfileService
	settings        *repo.SiteSettingRepository
	store           *storage.Client
	inflight        *InflightRegistry
	showcase        *repo.ShowcaseRepository
	orders          *repo.OrderRepository
	interval        time.Duration
	stalePending    time.Duration
	mediaPruneEvery time.Duration
	lastMediaPrune  time.Time
}

func NewMaintenanceService(tokens *repo.TokenRepository, tokenSvc *TokenService, events *repo.EventRepository, users *repo.UserRepository, refresh *RefreshProfileService, settings *repo.SiteSettingRepository, store *storage.Client, inflight *InflightRegistry, showcase *repo.ShowcaseRepository, orders *repo.OrderRepository) *MaintenanceService {
	return &MaintenanceService{
		tokens:          tokens,
		tokenSvc:        tokenSvc,
		events:          events,
		users:           users,
		refresh:         refresh,
		settings:        settings,
		store:           store,
		inflight:        inflight,
		showcase:        showcase,
		orders:          orders,
		interval:        60 * time.Second,
		stalePending:    600 * time.Second,
		mediaPruneEvery: 60 * time.Second,
	}
}

// Run drives the sweep every interval until ctx is cancelled. It runs one sweep
// immediately on startup so a freshly restarted process heals stuck state right
// away rather than after the first tick.
func (m *MaintenanceService) Run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	m.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tick(ctx)
		}
	}
}

// syncRecoveredQuota re-probes each just-recovered account so its displayed
// balance reflects the post-reset value (these providers only sync quota when
// accessed). krea additionally needs /app (Activate) to actually grant the daily
// free balance before billing-data reports it. Bounded concurrency avoids a
// thundering herd at the daily reset.
func (m *MaintenanceService) syncRecoveredQuota(accs []model.TokenAccount) {
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, acc := range accs {
		switch acc.Pool {
		case "chatgpt", "leonardo", "krea", "imagine":
		default:
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(a model.TokenAccount) {
			defer wg.Done()
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if a.Pool == "krea" && m.tokenSvc.krea != nil {
				m.tokenSvc.krea.Activate(ctx, a.Value)
			}
			_, _ = m.tokenSvc.Quota(ctx, a.Pool, a.ID)
		}(acc)
	}
	wg.Wait()
}

func (m *MaintenanceService) tick(ctx context.Context) {
	// 0. Auto-cancel unpaid recharge orders past their 30-min TTL.
	if m.orders != nil {
		if n, err := m.orders.ExpirePending(ctx, time.Now()); err != nil {
			log.Printf("maintenance: expire orders: %v", err)
		} else if n > 0 {
			log.Printf("maintenance: cancelled %d expired order(s)", n)
		}
	}

	// 1. Re-activate quota-exhausted tokens whose reset time has passed, then
	//    auto-sync their real balance — these providers only refresh quota when
	//    accessed, so recovery alone would leave a stale 0/—. For krea the sync
	//    must first load /app (Activate) to grant the daily free balance.
	if recovered, err := m.tokens.RecoverQuota(ctx); err != nil {
		log.Printf("maintenance: recover_quota: %v", err)
	} else if len(recovered) > 0 {
		log.Printf("maintenance: recovered %d quota token(s)", len(recovered))
		if m.tokenSvc != nil {
			go m.syncRecoveredQuota(recovered)
		}
	}

	// 1a. Roll the 恢复时间 marker of ACTIVE daily-reset accounts forward to the next
	//     future reset (same time-of-day, +1 day) so the column never shows a stale
	//     past time. Limited accounts are intentionally skipped (RecoverQuota owns
	//     their marker). adobe/leonardo/krea/imagine all renew daily.
	if _, err := m.tokens.RollResetMarkers(ctx, []string{"adobe", "leonardo", "krea", "imagine"}); err != nil {
		log.Printf("maintenance: roll_reset: %v", err)
	}

	// 1b. Runway tokens have no refresh — once the JWT expiry marker passes the
	//     token can only 401, so flip it to disabled+dead proactively instead of
	//     leaving a doomed account "active". Grok is intentionally NOT swept here:
	//     its reset marker is billingPeriodEnd (a credits-renewal date), not a
	//     death deadline — a grok sso keeps working past billingPeriodEnd, so
	//     expiring on it kills live accounts. Grok death is caught for real by the
	//     import-time FetchSession check and by marking dead on a 401 at use.
	for _, pool := range []string{"runway"} {
		if n, err := m.tokens.ExpireByReset(ctx, pool); err != nil {
			log.Printf("maintenance: expire_%s: %v", pool, err)
		} else if n > 0 {
			log.Printf("maintenance: expired %d %s token(s)", n, pool)
		}
	}

	// 1c. Poll Leonardo get-session every five minutes like the web app, and
	//     proactively renew krea/imagine sessions ~10min before expiry. Per-account
	//     due markers keep the one-minute sweep from repeatedly hitting upstream.
	if m.tokenSvc != nil {
		m.tokenSvc.RefreshExpiringTokens(ctx)
		// 1d. Once-per-day krea /app activation for accounts not yet synced since the
		//     daily reset — krea only grants the free balance after /app loads, so an
		//     always-active account (never went 限额) would otherwise read 0 / 402
		//     after each reset. Self-guarded + background; no-op once all are done.
		m.tokenSvc.ActivateKreaDue(ctx)
		// 1e. Re-validate grok accounts: an empty /rest/subscriptions (or 401)
		//     means the membership lapsed → disable+dead; otherwise re-sync the
		//     credits balance and 恢复时间 (from the credits' weekly reset).
		m.tokenSvc.RefreshGrokLiveness(ctx)
	}

	// 2. Auto-renew Adobe cookies whose refresh interval has elapsed.
	if m.refresh != nil {
		if n, err := m.refresh.RefreshDue(ctx); err != nil {
			log.Printf("maintenance: refresh_due: %v", err)
		} else if n > 0 {
			log.Printf("maintenance: refreshed %d cookie profile(s)", n)
		}
	}

	// 3. Fail long-pending events so they stop blocking the per-user gate, and
	//    refund the credits debited up-front for each abandoned generation (the
	//    normal failure-refund path never ran for a process-restart orphan).
	if purged, err := m.events.PurgeStale(ctx, m.stalePending); err != nil {
		log.Printf("maintenance: purge_stale: %v", err)
	} else if len(purged) > 0 {
		refunded := 0
		cancelled := 0
		for _, e := range purged {
			// Stop the generation goroutine if it's still running, so it doesn't
			// keep grinding for minutes and surface a late "success" on this
			// just-abandoned event.
			if m.inflight != nil && m.inflight.Cancel(e.ID) {
				cancelled++
			}
			// Attribute the abandoned failure back to the account it was using
			// (the normal markTokenFailure path never ran for an orphaned job).
			if e.AccountID != "" {
				if err := m.tokens.IncrementFail(ctx, e.AccountID); err != nil {
					log.Printf("maintenance: fail-count abandoned event %s (account %s): %v", e.ID, e.AccountID, err)
				}
			}
			if e.UserID == "" || e.Cost <= 0 {
				continue
			}
			// Exactly-once: only refund if we win the claim (the in-flight request
			// may have already refunded itself on its own failure path).
			claimed, err := m.events.MarkRefunded(ctx, e.ID)
			if err != nil {
				log.Printf("maintenance: claim refund %s: %v", e.ID, err)
				continue
			}
			if !claimed {
				continue
			}
			if _, err := m.users.AdjustCredits(ctx, e.UserID, e.Cost); err != nil {
				log.Printf("maintenance: refund abandoned event %s (user %s, %.0f): %v", e.ID, e.UserID, e.Cost, err)
			} else {
				refunded++
			}
		}
		log.Printf("maintenance: marked %d stale pending event(s) failed, refunded %d, cancelled %d in-flight", len(purged), refunded, cancelled)
	}

	// 4. Enforce the admin-configured log retention window.
	m.pruneLogs(ctx)

	// 5. Enforce the media retention window. Runs every 60s like the log prune;
	//    mediaPruneEvery still gates it in case the interval is ever shortened.
	if time.Since(m.lastMediaPrune) >= m.mediaPruneEvery {
		m.pruneMedia(ctx)
		m.lastMediaPrune = time.Now()
	}
}

func (m *MaintenanceService) pruneLogs(ctx context.Context) {
	days := m.retentionDays(ctx, "logs.retention_days")
	if days <= 0 {
		return
	}
	if _, err := m.events.PurgeOlderThan(ctx, time.Duration(days)*24*time.Hour); err != nil {
		log.Printf("maintenance: purge_older_than: %v", err)
	}
}

func (m *MaintenanceService) pruneMedia(ctx context.Context) {
	if m.store == nil || !m.store.Configured() {
		return
	}
	days := m.retentionDays(ctx, "media.retention_days")
	if days <= 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	objs, err := m.store.List(ctx, "")
	if err != nil {
		log.Printf("maintenance: list media: %v", err)
		return
	}
	// Files referenced by the homepage showcase are kept forever, no matter how
	// old — deleting them would break the public landing page.
	var pinned map[string]struct{}
	if m.showcase != nil {
		if pinned, err = m.showcase.PublicFileSet(ctx); err != nil {
			log.Printf("maintenance: showcase file set: %v", err)
			pinned = nil
		}
	}
	// The site logo is permanent too — pin it like a showcase image so the
	// retention sweep never deletes it. New installs persist a public R2 URL;
	// old installs may still have the legacy "/images/<key>" form.
	if logo, _ := m.settings.GetValue(ctx, "site.logo"); strings.TrimSpace(logo) != "" {
		if pinned == nil {
			pinned = map[string]struct{}{}
		}
		if key, ok := m.store.KeyFromPublicURL(logo); ok {
			pinned[key] = struct{}{}
		} else {
			pinned[strings.TrimPrefix(strings.TrimLeft(logo, "/"), "images/")] = struct{}{}
		}
	}
	removed, skipped := 0, 0
	var clearedKeys []string
	for _, o := range objs {
		if !o.LastModified.Before(cutoff) {
			continue
		}
		if _, ok := pinned[strings.TrimLeft(o.Key, "/")]; ok {
			skipped++
			continue
		}
		if err := m.store.Delete(ctx, o.Key); err != nil {
			log.Printf("maintenance: delete %s: %v", o.Key, err)
			continue
		}
		removed++
		// event_log.file stores the same key — blank those rows so the log views
		// don't dangle a 404 preview.
		clearedKeys = append(clearedKeys, o.Key)
	}
	if removed > 0 || skipped > 0 {
		log.Printf("maintenance: pruned %d expired media object(s), kept %d showcase-pinned", removed, skipped)
	}
	if len(clearedKeys) > 0 {
		if n, err := m.events.ClearFiles(ctx, clearedKeys); err != nil {
			log.Printf("maintenance: clear_files: %v", err)
		} else if n > 0 {
			log.Printf("maintenance: cleared file ref on %d log row(s)", n)
		}
	}
}

func (m *MaintenanceService) retentionDays(ctx context.Context, key string) int {
	raw, err := m.settings.GetValue(ctx, key)
	if err != nil {
		return 0
	}
	days, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || days <= 0 {
		return 0
	}
	return days
}
