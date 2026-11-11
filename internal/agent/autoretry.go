package agent

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/errhint"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providers/providerio"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// Unified auto-retry: one policy per model call. Every StreamCompletion
// invocation inside a turn draws from the same budget of maxModelCallAttempts
// attempts (the first try plus retries) — the loop marks the call context
// (providerio.WithCallerRetry) so the provider's HTTP layer issues exactly
// one attempt per call, and status/pre-send retries are the loop's job now,
// which is what makes the 10 visible attempts the 10 requests in total.
// Calls made outside the loop (titles, compaction summaries) keep the
// provider's internal retries. The budget resets on every new model call: a
// turn that succeeds spends nothing of the next turn's attempts.
const maxModelCallAttempts = 10

// autoRetryBaseDelay is the first wait before a retry, doubled per subsequent
// retry: 7.5s, 15s, 30s, 1m, 2m, 4m, then capped.
const autoRetryBaseDelay = 7500 * time.Millisecond

// autoRetryMaxDelay caps a single wait so a long outage or a hostile
// Retry-After hint never parks a run for more than 5 minutes per wait.
const autoRetryMaxDelay = 5 * time.Minute

// RetryWaitInfo describes one scheduled model-call retry for surfaces. The
// agent reports every wait through Options.OnRetryWait; Active false settles a
// previous wait (the attempt finally ran or the budget ran out).
type RetryWaitInfo struct {
	Attempt         int
	MaxAttempts     int
	Wait            time.Duration
	Summary         string
	DiscardedOutput bool
	Active          bool
}

// isRetryableModelError reports whether a failed model call is worth
// re-issuing: transport/network failures, timeouts, content stalls with no
// output yet, the retryable HTTP statuses (408, 425, 429, 500, 502, 503, 504,
// 529) and upstream errors arriving mid-stream. Repeating a 5xx may rebill the
// failed attempt — accepted deliberately, it is what the user would do by
// hand. Everything else fails fast: auth/billing/model errors, context-limit
// (the compactor owns those), image rejections (retrying loops forever on a
// text-only model), and cancellations/permission stops. A cancelled context
// never retries, so Esc/Ctrl+C always wins immediately.
func isRetryableModelError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if msg == "" {
		return false
	}
	if isContextLimitError(msg) || isImageRejectionError(err) {
		return false
	}
	// Permanent request errors: retrying cannot help and may loop or rebill.
	if errhint.HasStatusCode(msg, "400", "401", "402", "403", "404") {
		return false
	}
	for _, needle := range []string{
		"unauthorized",
		"invalid api key",
		"forbidden",
		"model_not_allowed",
		"model not allowed",
		"model not found",
		"insufficient",
		"payment required",
		"saldo insuficiente",
		"cancel",
		"permission",
		"denied",
		"not approved",
		"approval",
	} {
		if strings.Contains(msg, needle) {
			return false
		}
	}
	// A typed provider failure decides by status code: the code is exact, so
	// it outranks substring needles. Billing exhaustion stays excluded above
	// ("insufficient") even when the provider maps it onto a 429.
	var statusErr *providerio.StatusError
	if errors.As(err, &statusErr) && statusErr.StatusCode != 0 {
		switch statusErr.StatusCode {
		case 408, 425, 429, 500, 502, 503, 504, 529:
			return true
		default:
			return false
		}
	}
	if errhint.HasStatusCode(msg, "408", "425", "429", "500", "502", "503", "504", "529") {
		return true
	}
	if isStreamTimeoutError(msg) {
		return true
	}
	for _, needle := range []string{
		"rate limit",
		"too many requests",
		"slow down",
		"overloaded",
		"service unavailable",
		"bad gateway",
		"gateway timeout",
		"internal server error",
		"upstream",
		"proxy",
		"eof",
		"connection reset",
		"connection refused",
		"connection aborted",
		"connection closed",
		"broken pipe",
		"timeout",
		"timed out",
		"temporarily unavailable",
		"server closed",
		"unexpected end",
		"network",
		"no such host",
		"name resolution",
		"dns",
		"tls handshake",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// autoRetryDelay is the deterministic wait before retry number retry
// (1-based): the base doubled per retry, capped at autoRetryMaxDelay. A
// positive server Retry-After wins, also capped.
func autoRetryDelay(retry int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > autoRetryMaxDelay {
			return autoRetryMaxDelay
		}
		return retryAfter
	}
	wait := autoRetryBaseDelay
	for i := 1; i < retry; i++ {
		if wait >= autoRetryMaxDelay {
			return autoRetryMaxDelay
		}
		wait *= 2
	}
	if wait > autoRetryMaxDelay {
		return autoRetryMaxDelay
	}
	return wait
}

// autoRetryJitter spreads a wait by ±10% so concurrent runs (swarm members, a
// fleet on the same outage) do not retry in lockstep.
func autoRetryJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	tenth := d / 10
	return d - tenth + time.Duration(rand.Int63n(int64(tenth)*2+1))
}

// parseRetryAfterHint extracts a server Retry-After delay in seconds from an
// error message ("retry after 30", "Retry-After: 12", "retry in 45s"), or 0
// when absent. Capped at autoRetryMaxDelay; digit-boundary matched so an
// incidental number (a 4290ms latency, a request id) is never read as one.
func parseRetryAfterHint(msg string) time.Duration {
	lowered := strings.ToLower(msg)
	for _, marker := range []string{"retry-after:", "retry-after", "retry after", "retry in"} {
		rest := lowered
		for {
			idx := strings.Index(rest, marker)
			if idx < 0 {
				break
			}
			rest = rest[idx+len(marker):]
			digits := ""
			for _, r := range strings.TrimLeft(rest, " :") {
				if r >= '0' && r <= '9' {
					digits += string(r)
					continue
				}
				break
			}
			if digits == "" {
				continue
			}
			var seconds int64
			for _, r := range digits {
				seconds = seconds*10 + int64(r-'0')
				if seconds > int64(autoRetryMaxDelay/time.Second) {
					return autoRetryMaxDelay
				}
			}
			if seconds > 0 {
				return time.Duration(seconds) * time.Second
			}
		}
	}
	return 0
}

// shortRetrySummary condenses a model error to one display line for the TUI
// countdown and notices.
func shortRetrySummary(msg string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(msg), "\n")
	line = strings.Join(strings.Fields(line), " ")
	const maxSummaryRunes = 120
	if runes := []rune(line); len(runes) > maxSummaryRunes {
		return string(runes[:maxSummaryRunes]) + "…"
	}
	return line
}

// formatRetryWait renders a wait compactly: "45s", "1m 12s".
func formatRetryWait(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm %02ds", int(d/time.Minute), int(d.Seconds())%60)
}

// modelCallRetrier carries one model call's retry budget: every
// StreamCompletion invocation in the turn draws from it, so connect failures,
// mid-stream upstream errors and content stalls share the same 10 attempts.
type modelCallRetrier struct {
	used    int
	waiting bool
	sleep   func(context.Context, time.Duration) error
	jitter  func(time.Duration) time.Duration
	notify  func(attempt, max int, wait time.Duration, errMsg string, discarded bool)
	onWait  func(RetryWaitInfo)
}

func newModelCallRetrier(options Options) *modelCallRetrier {
	retrier := &modelCallRetrier{
		sleep:  sleepWithContext,
		jitter: autoRetryJitter,
		onWait: options.OnRetryWait,
	}
	if options.RetrySleep != nil {
		retrier.sleep = options.RetrySleep
	}
	if options.OnReasoning != nil {
		onReasoning := options.OnReasoning
		retrier.notify = func(attempt, max int, wait time.Duration, errMsg string, discarded bool) {
			notice := fmt.Sprintf("\n[%s · tentando novamente em %s… %d/%d]\n",
				shortRetrySummary(errMsg), formatRetryWait(wait), attempt, max)
			if discarded {
				notice = fmt.Sprintf("\n[%s · saída parcial descartada — tentando novamente em %s… %d/%d]\n",
					shortRetrySummary(errMsg), formatRetryWait(wait), attempt, max)
			}
			onReasoning(notice)
		}
	}
	return retrier
}

// exhausted reports whether the budget spent all its attempts.
func (r *modelCallRetrier) exhausted() bool {
	return r.used >= maxModelCallAttempts
}

// settle clears a previously reported wait once the attempt finally runs or
// the budget runs out.
func (r *modelCallRetrier) settle() {
	if !r.waiting {
		return
	}
	r.waiting = false
	if r.onWait != nil {
		r.onWait(RetryWaitInfo{Active: false})
	}
}

// singleStream issues one model request, spending one budget attempt. The
// context carries the caller-retry marker so the provider's HTTP layer makes
// exactly one attempt: this budget is the only budget.
func (r *modelCallRetrier) singleStream(ctx context.Context, provider Provider, request qgruntime.CompletionRequest) (<-chan qgruntime.StreamEvent, error) {
	r.used++
	stream, err := provider.StreamCompletion(providerio.WithCallerRetry(ctx), request)
	if err == nil {
		r.settle()
		return stream, nil
	}
	return nil, err
}

// openStream returns a live stream, retrying retryable connect failures inside
// the call's budget. A non-retryable error returns as-is; an exhausted budget
// returns the original message with the attempt count.
func (r *modelCallRetrier) openStream(ctx context.Context, provider Provider, request qgruntime.CompletionRequest) (<-chan qgruntime.StreamEvent, error) {
	for {
		stream, err := r.singleStream(ctx, provider, request)
		if err == nil {
			return stream, nil
		}
		proceed, waitErr := r.waitAfterError(ctx, err, false)
		if waitErr != nil {
			return nil, waitErr
		}
		if !proceed {
			return nil, err
		}
	}
}

// waitAfterError schedules the wait before the next attempt for a failed
// stream (a connect error or a collected stream error). It returns proceed
// false with a nil error for non-retryable failures (the caller runs its
// existing error path); a non-nil error ends the call now — the exhausted
// budget (original message plus the attempt count) or the context error.
func (r *modelCallRetrier) waitAfterError(ctx context.Context, err error, discarded bool) (bool, error) {
	if err == nil {
		return false, nil
	}
	if ctx.Err() != nil {
		r.settle()
		return false, ctx.Err()
	}
	if !isRetryableModelError(ctx, err) {
		return false, nil
	}
	if r.exhausted() {
		r.settle()
		msg := err.Error()
		return false, fmt.Errorf("%s (%d/%d tentativas)", msg, maxModelCallAttempts, maxModelCallAttempts)
	}
	// The server's delay travels structured on the provider's typed error;
	// text parsing is only the fallback for untyped failures.
	retryAfter := time.Duration(0)
	var statusErr *providerio.StatusError
	if errors.As(err, &statusErr) {
		retryAfter = statusErr.RetryAfter
	}
	if retryAfter <= 0 {
		retryAfter = parseRetryAfterHint(strings.ToLower(err.Error()))
	}
	wait := r.jitter(autoRetryDelay(r.used, retryAfter))
	attempt := r.used + 1
	summary := shortRetrySummary(err.Error())
	if r.notify != nil {
		r.notify(attempt, maxModelCallAttempts, wait, err.Error(), discarded)
	}
	if r.onWait != nil {
		r.onWait(RetryWaitInfo{
			Attempt:         attempt,
			MaxAttempts:     maxModelCallAttempts,
			Wait:            wait,
			Summary:         summary,
			DiscardedOutput: discarded,
			Active:          true,
		})
	}
	r.waiting = true
	if sleepErr := r.sleep(ctx, wait); sleepErr != nil {
		r.settle()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		return false, sleepErr
	}
	return true, nil
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
