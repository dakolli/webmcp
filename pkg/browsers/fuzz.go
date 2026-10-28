package browsers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// fuzzMaxRequests bounds the matrix requests; the baseline probe is extra.
	fuzzMaxRequests = 100
	// fuzzConcurrency bounds how many variants are in flight at once.
	fuzzConcurrency = 5
	// fuzzRequestTimeout bounds one variant request, redirects included.
	fuzzRequestTimeout = 10 * time.Second
	// fuzzMaxPoints bounds the injection points in one matrix.
	fuzzMaxPoints = 20
	// fuzzMaxPayloads bounds the payload list in one matrix.
	fuzzMaxPayloads = 50
	// fuzzMaxPayloadRunes bounds one payload value.
	fuzzMaxPayloadRunes = 200
)

const (
	// fuzzRuleStatus is the anomaly rule for a status unlike the baseline.
	fuzzRuleStatus = "status_changed"
	// fuzzRuleSize is the anomaly rule for a body size off by more than 10%.
	fuzzRuleSize = "size_delta"
	// fuzzRuleTime is the anomaly rule for a duration off by more than 3x.
	fuzzRuleTime = "time_delta"
)

// FuzzStrategy selects how a fuzz plan pairs injection points with payloads.
type FuzzStrategy string

const (
	// FuzzSniper injects one point at a time across every payload.
	FuzzSniper FuzzStrategy = "sniper"
	// FuzzPitchfork pairs the nth point with the nth payload, stopping at the
	// shorter list.
	FuzzPitchfork FuzzStrategy = "pitchfork"
	// FuzzClusterbomb injects a payload at every point and covers every
	// combination, so its request count is payloads raised to points.
	FuzzClusterbomb FuzzStrategy = "clusterbomb"
)

// FuzzLocation names where a payload is injected into the captured request.
type FuzzLocation string

const (
	// FuzzQuery replaces or adds a URL query parameter.
	FuzzQuery FuzzLocation = "query"
	// FuzzBody replaces or adds a field in the request body.
	FuzzBody FuzzLocation = "body"
	// FuzzHeader replaces or adds a request header.
	FuzzHeader FuzzLocation = "header"
	// FuzzCookie replaces or adds a cookie for the target host.
	FuzzCookie FuzzLocation = "cookie"
)

// FuzzPoint is one place in the captured request a payload can be injected.
type FuzzPoint struct {
	// Param is the query parameter, body field, header name or cookie name.
	Param string `json:"param"`
	// Location is where Param lives in the request.
	Location FuzzLocation `json:"location"`
}

// FuzzSpec selects one captured request and the injection matrix to run over it.
type FuzzSpec struct {
	// Target is a network sequence number or a unique substring of the URL.
	Target string
	// Strategy defaults to sniper when empty.
	Strategy FuzzStrategy
	// Points are the injection points, capped at 20.
	Points []FuzzPoint
	// Payloads are the values, capped at 50 values of 200 runes each.
	Payloads []string
	// CheckScope authorizes a URL before the request leaves; the surface wires
	// the manager's scope check here.
	CheckScope func(rawURL string) error
	// MaxBytes caps each returned body only; the true body size is still
	// measured for the rows and the anomaly rules. Zero uses the replay
	// ceiling.
	MaxBytes int64
}

// FuzzRun is one executed variant: the payload, the point it hit and the
// response outcome. Seq starts at 1 and follows plan order.
type FuzzRun struct {
	// Seq is the one-based plan position, stable across concurrent completion.
	Seq int `json:"seq"`
	// Payload is the injected value, or the per-point values joined by " | "
	// for a clusterbomb row.
	Payload string `json:"payload"`
	// Point is the injected parameter, or the parameters joined by "," for a
	// clusterbomb row.
	Point string `json:"point"`
	// Status is the final HTTP status.
	Status int `json:"status"`
	// MS is the whole request duration, redirects included.
	MS int64 `json:"ms"`
	// Bytes is the true response body size measured before the cap, so the
	// size_delta rule compares real sizes even when max_bytes is set.
	Bytes int64 `json:"bytes"`
	// BodyCut reports a returned body truncated at the cap; Bytes still
	// carries the true size.
	BodyCut bool `json:"body_cut"`
	// BodyRedactions is the count the replay redactor reported for the body.
	BodyRedactions int `json:"body_redactions"`
}

// FuzzBaseline is the unmodified request every variant is compared with. It is
// probed twice so the compared timing is warm: ColdMS is the first sample that
// paid for connection setup, MS the second.
type FuzzBaseline struct {
	// Status is the baseline HTTP status from the warm probe.
	Status int `json:"status"`
	// Bytes is the true baseline body size measured before the cap.
	Bytes int64 `json:"bytes"`
	// ColdMS is the first, warming probe's duration.
	ColdMS int64 `json:"cold_ms"`
	// MS is the second, warm probe's duration that every variant is compared
	// against.
	MS int64 `json:"ms"`
}

// timingSpread reports the gap between the cold and warm probes. A variant whose
// duration differs from MS by less than this is baseline noise, not an anomaly.
func (b FuzzBaseline) timingSpread() int64 {
	spread := b.ColdMS - b.MS
	if spread < 0 {
		return -spread
	}
	return spread
}

// FuzzAnomaly is one rule hit on one row.
type FuzzAnomaly struct {
	// Seq is the row the rule fired on.
	Seq int `json:"seq"`
	// Rule names the rule that fired.
	Rule string `json:"rule"`
	// Why states the observed value and the baseline.
	Why string `json:"why"`
}

// FuzzSummary carries the baseline and every anomaly found.
type FuzzSummary struct {
	// Baseline is the unmodified request result.
	Baseline FuzzBaseline `json:"baseline"`
	// Anomalies lists every rule hit in row order.
	Anomalies []FuzzAnomaly `json:"anomalies"`
}

// FuzzResult is one fuzz run: the rows in plan order plus the summary.
type FuzzResult struct {
	// Runs lists the finished rows, in plan order.
	Runs []FuzzRun `json:"runs"`
	// Summary is the baseline and the anomalies.
	Summary FuzzSummary `json:"summary"`
}

// FuzzBoundsError reports a fuzz matrix over a fixed bound. Computed and Bound
// are decimal strings so a clusterbomb that would overflow an int is still
// reported exactly.
type FuzzBoundsError struct {
	// Unit names what the bound counts, e.g. "requests".
	Unit string
	// Computed is the matrix size that was computed.
	Computed string
	// Bound is the fixed limit that was exceeded.
	Bound string
}

// Error names the unit, the computed size and the bound.
func (e *FuzzBoundsError) Error() string {
	return fmt.Sprintf(
		"replay_fuzz: %s computed %s, over the %s bound",
		e.Unit,
		e.Computed,
		e.Bound,
	)
}

// Hint names the two knobs that fit an over-bound matrix: a smaller payload
// list, or the sniper strategy whose points*payloads plan is the cheapest.
func (e *FuzzBoundsError) Hint() string {
	if e.Unit == "requests" {
		return "lower the payload count or the point count, or use strategy sniper (points*payloads) to stay under the 100-request bound"
	}
	return fmt.Sprintf("lower the %s to the %s bound and retry", e.Unit, e.Bound)
}

// ReplayFuzz runs a bounded matrix of variants over one captured request. It
// validates the fixed caps and the computed size before any request leaves,
// checks scope before the baseline, probes the baseline twice so the compared
// timing is warm rather than the slowest sample of the run, then fans the
// variants out five at a time with a ten-second budget each. Every variant goes
// through the replay seam, so scope, the per-host rate limit and body redaction
// apply to each one. Rows keep plan order with Seq starting at 1 even though the
// fan-out completes out of order; a cancelled or failed run returns the rows
// that finished together with the error.
func (p *Page) ReplayFuzz(ctx context.Context, spec FuzzSpec) (FuzzResult, error) {
	if spec.Strategy == "" {
		spec.Strategy = FuzzSniper
	}
	if _, err := fuzzValidate(spec); err != nil {
		return FuzzResult{}, err
	}

	entries, _ := p.NetworkEntries()
	entry, err := matchReplayEntry(entries, spec.Target)
	if err != nil {
		return FuzzResult{}, fmt.Errorf("replay_fuzz: %w", err)
	}
	target, err := replayTargetURL(entry)
	if err != nil {
		return FuzzResult{}, err
	}
	if err := replayScope(ReplaySpec{CheckScope: spec.CheckScope}, target.String()); err != nil {
		return FuzzResult{}, fmt.Errorf("replay_fuzz: %w", err)
	}
	if err := p.browser.CheckRequestScope(target.String()); err != nil {
		return FuzzResult{}, fmt.Errorf("replay_fuzz: %s refused: %w", target.String(), err)
	}

	req := fuzzRequest{
		entry:      entry,
		checkScope: spec.CheckScope,
		maxBytes:   spec.MaxBytes,
	}
	if fuzzNeedsBody(spec.Points) {
		body, err := p.fuzzCapturedBody(ctx, entry)
		if err != nil {
			return FuzzResult{}, err
		}
		req.body = body
	}

	probe := func() (ReplayResult, error) {
		probeCtx, cancel := context.WithTimeout(ctx, fuzzRequestTimeout)
		defer cancel()
		return p.replayEntry(probeCtx, entry, ReplaySpec{
			CheckScope: spec.CheckScope,
			MaxBytes:   spec.MaxBytes,
		})
	}
	base, err := fuzzWarmBaseline(probe)
	if err != nil {
		return FuzzResult{}, fmt.Errorf("replay_fuzz: baseline: %w", err)
	}

	result := FuzzResult{
		Runs: []FuzzRun{},
		Summary: FuzzSummary{
			Baseline:  base,
			Anomalies: []FuzzAnomaly{},
		},
	}

	run := func(rctx context.Context, variant fuzzVariant) (FuzzRun, error) {
		spec, err := req.spec(variant)
		if err != nil {
			return FuzzRun{}, err
		}
		res, err := p.replayEntry(rctx, entry, spec)
		if err != nil {
			return FuzzRun{}, err
		}
		return fuzzRunOf(variant, res), nil
	}
	plan := fuzzPlan(spec.Strategy, spec.Points, spec.Payloads)
	rows, runErr := fuzzExecute(ctx, plan, run)
	result.Runs = rows
	result.Summary.Anomalies = fuzzAnomalies(rows, result.Summary.Baseline)
	if runErr != nil {
		return result, fmt.Errorf("replay_fuzz: %w", runErr)
	}
	return result, nil
}

// fuzzVariant is one planned request: the points it injects and the payload
// aligned with each point.
type fuzzVariant struct {
	point   string
	payload string
	points  []FuzzPoint
	values  []string
}

// fuzzRequest is the captured request a plan variant is applied to.
type fuzzRequest struct {
	entry      NetworkEntry
	body       string
	checkScope func(rawURL string) error
	maxBytes   int64
}

// spec renders one variant as a replay spec over the captured request.
func (r fuzzRequest) spec(variant fuzzVariant) (ReplaySpec, error) {
	spec := ReplaySpec{
		CheckScope: r.checkScope,
		MaxBytes:   r.maxBytes,
	}
	rawURL := r.entry.URL
	body := r.body
	headers := make(map[string]string, len(variant.points))
	cookies := make(map[string]string, len(variant.points))
	urlChanged, bodyChanged := false, false
	for i, point := range variant.points {
		value := variant.values[i]
		switch point.Location {
		case FuzzQuery:
			u, err := url.Parse(rawURL)
			if err != nil {
				return ReplaySpec{}, fmt.Errorf("replay_fuzz: query point %q: %w", point.Param, err)
			}
			query := u.Query()
			query.Set(point.Param, value)
			u.RawQuery = query.Encode()
			rawURL = u.String()
			urlChanged = true
		case FuzzBody:
			updated, err := fuzzInjectBody(
				body,
				point.Param,
				value,
				capturedHeader(r.entry.ReqHeaders, "Content-Type"),
			)
			if err != nil {
				return ReplaySpec{}, err
			}
			body = updated
			bodyChanged = true
		case FuzzHeader:
			headers[point.Param] = value
		case FuzzCookie:
			cookies[point.Param] = value
		default:
			return ReplaySpec{}, fmt.Errorf("replay_fuzz: unknown location %q", point.Location)
		}
	}
	if urlChanged {
		spec.URL = rawURL
	}
	if len(headers) > 0 {
		spec.Headers = headers
	}
	if len(cookies) > 0 {
		spec.Cookies = cookies
	}
	if bodyChanged {
		spec.Body = body
		spec.BodySet = true
	}
	return spec, nil
}

// fuzzWarmBaseline probes the unmodified request twice. The first sample warms
// the connection and its timing is reported as cold_ms; the second supplies the
// timing every variant is compared against. A single cold sample would be the
// slowest of the run and flag healthy variants.
func fuzzWarmBaseline(probe func() (ReplayResult, error)) (FuzzBaseline, error) {
	cold, err := probe()
	if err != nil {
		return FuzzBaseline{}, err
	}
	warm, err := probe()
	if err != nil {
		return FuzzBaseline{}, fmt.Errorf("warm probe: %w", err)
	}
	return fuzzBaselineOf(warm, cold), nil
}

// fuzzBaselineOf maps the warm probe to the summary baseline, keeping the cold
// probe's timing and the body size measured before the cap.
func fuzzBaselineOf(warm, cold ReplayResult) FuzzBaseline {
	return FuzzBaseline{
		Status: warm.Status,
		Bytes:  warm.BodyBytes,
		ColdMS: cold.MS,
		MS:     warm.MS,
	}
}

// fuzzRunOf maps a planned variant and its replay result to one row, keeping
// the true body size while body_cut marks a capped return.
func fuzzRunOf(variant fuzzVariant, res ReplayResult) FuzzRun {
	return FuzzRun{
		Payload:        variant.payload,
		Point:          variant.point,
		Status:         res.Status,
		MS:             res.MS,
		Bytes:          res.BodyBytes,
		BodyCut:        res.Truncated,
		BodyRedactions: res.BodyRedactions,
	}
}

// fuzzValidate enforces the fixed caps and returns the computed request count.
func fuzzValidate(spec FuzzSpec) (*big.Int, error) {
	strategy := spec.Strategy
	if strategy == "" {
		strategy = FuzzSniper
	}
	switch strategy {
	case FuzzSniper, FuzzPitchfork, FuzzClusterbomb:
	default:
		return nil, fmt.Errorf(
			"replay_fuzz: unknown strategy %q — use sniper, pitchfork or clusterbomb",
			strategy,
		)
	}
	if len(spec.Points) == 0 {
		return nil, fmt.Errorf("replay_fuzz: at least one injection point is required")
	}
	if len(spec.Payloads) == 0 {
		return nil, fmt.Errorf("replay_fuzz: at least one payload is required")
	}
	for i, point := range spec.Points {
		if strings.TrimSpace(point.Param) == "" {
			return nil, fmt.Errorf("replay_fuzz: injection point %d has an empty param", i+1)
		}
		switch point.Location {
		case FuzzQuery, FuzzBody, FuzzHeader, FuzzCookie:
		default:
			return nil, fmt.Errorf(
				"replay_fuzz: injection point %q has unknown location %q — use query, body, header or cookie",
				point.Param,
				point.Location,
			)
		}
	}
	for i, payload := range spec.Payloads {
		if n := utf8.RuneCountInString(payload); n > fuzzMaxPayloadRunes {
			return nil, &FuzzBoundsError{
				Unit:     fmt.Sprintf("payload %d runes", i+1),
				Computed: strconv.Itoa(n),
				Bound:    strconv.Itoa(fuzzMaxPayloadRunes),
			}
		}
	}
	count := fuzzCount(strategy, len(spec.Points), len(spec.Payloads))
	if count.Cmp(big.NewInt(int64(fuzzMaxRequests))) > 0 {
		return nil, &FuzzBoundsError{
			Unit:     "requests",
			Computed: count.String(),
			Bound:    strconv.Itoa(fuzzMaxRequests),
		}
	}
	if len(spec.Points) > fuzzMaxPoints {
		return nil, &FuzzBoundsError{
			Unit:     "injection points",
			Computed: strconv.Itoa(len(spec.Points)),
			Bound:    strconv.Itoa(fuzzMaxPoints),
		}
	}
	if len(spec.Payloads) > fuzzMaxPayloads {
		return nil, &FuzzBoundsError{
			Unit:     "payloads",
			Computed: strconv.Itoa(len(spec.Payloads)),
			Bound:    strconv.Itoa(fuzzMaxPayloads),
		}
	}
	return count, nil
}

// fuzzCount computes the request count a strategy needs without building the
// plan, so an oversized matrix is refused before any allocation or request.
func fuzzCount(strategy FuzzStrategy, points, payloads int) *big.Int {
	p := big.NewInt(int64(points))
	l := big.NewInt(int64(payloads))
	switch strategy {
	case FuzzSniper:
		return new(big.Int).Mul(p, l)
	case FuzzPitchfork:
		return big.NewInt(int64(min(points, payloads)))
	case FuzzClusterbomb:
		return new(big.Int).Exp(l, p, nil)
	default:
		return big.NewInt(0)
	}
}

// fuzzPlan builds the variants in strategy order. It is only called after
// fuzzValidate has bounded the size.
func fuzzPlan(strategy FuzzStrategy, points []FuzzPoint, payloads []string) []fuzzVariant {
	switch strategy {
	case FuzzPitchfork:
		n := min(len(points), len(payloads))
		out := make([]fuzzVariant, 0, n)
		for i := range n {
			out = append(out, fuzzVariant{
				point:   points[i].Param,
				payload: payloads[i],
				points:  []FuzzPoint{points[i]},
				values:  []string{payloads[i]},
			})
		}
		return out
	case FuzzClusterbomb:
		return fuzzCombinations(points, payloads)
	default:
		out := make([]fuzzVariant, 0, len(points)*len(payloads))
		for _, point := range points {
			for _, payload := range payloads {
				out = append(out, fuzzVariant{
					point:   point.Param,
					payload: payload,
					points:  []FuzzPoint{point},
					values:  []string{payload},
				})
			}
		}
		return out
	}
}

// fuzzCombinations builds the cartesian product with the first point as the
// most significant position, so order is deterministic.
func fuzzCombinations(points []FuzzPoint, payloads []string) []fuzzVariant {
	params := make([]string, len(points))
	for i, point := range points {
		params[i] = point.Param
	}
	label := strings.Join(params, ",")
	out := make([]fuzzVariant, 0, fuzzMaxRequests)
	index := make([]int, len(points))
	for {
		values := make([]string, len(points))
		for i, n := range index {
			values[i] = payloads[n]
		}
		out = append(out, fuzzVariant{
			point:   label,
			payload: strings.Join(values, " | "),
			points:  points,
			values:  values,
		})
		i := len(index) - 1
		for i >= 0 {
			index[i]++
			if index[i] < len(payloads) {
				break
			}
			index[i] = 0
			i--
		}
		if i < 0 {
			break
		}
	}
	return out
}

// fuzzExecute runs one request per variant five at a time and returns rows in
// plan order with Seq assigned from 1. The first failure cancels the fan-out;
// rows that already finished are still returned with the error.
func fuzzExecute(
	ctx context.Context,
	plan []fuzzVariant,
	run func(context.Context, fuzzVariant) (FuzzRun, error),
) ([]FuzzRun, error) {
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	rows := make([]FuzzRun, len(plan))
	filled := make([]bool, len(plan))
	sem := make(chan struct{}, fuzzConcurrency)
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)

loop:
	for i, variant := range plan {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			once.Do(func() { firstErr = ctx.Err() })
			break loop
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := ctx.Err(); err != nil {
				once.Do(func() { firstErr = err })
				return
			}
			rctx, cancel := context.WithTimeout(ctx, fuzzRequestTimeout)
			defer cancel()
			row, err := run(rctx, variant)
			if err != nil {
				once.Do(func() {
					firstErr = fmt.Errorf("row %d (%s): %w", i+1, variant.point, err)
					stop()
				})
				return
			}
			row.Seq = i + 1
			rows[i] = row
			filled[i] = true
		}()
	}
	wg.Wait()

	out := make([]FuzzRun, 0, len(plan))
	for i, row := range rows {
		if filled[i] {
			out = append(out, row)
		}
	}
	return out, firstErr
}

// fuzzAnomalies compares every row with the baseline. It is the single home of
// the rule list: status_changed, size_delta above 10 percent and time_delta
// above 3x in either direction. A time_delta also has to clear the baseline's
// own cold-to-warm spread, so two probes that disagree by more than the run's
// deviation suppress the rule rather than flag noise.
func fuzzAnomalies(rows []FuzzRun, base FuzzBaseline) []FuzzAnomaly {
	out := make([]FuzzAnomaly, 0)
	for _, row := range rows {
		if row.Status != base.Status {
			out = append(out, FuzzAnomaly{
				Seq:  row.Seq,
				Rule: fuzzRuleStatus,
				Why:  fmt.Sprintf("status %d, baseline %d", row.Status, base.Status),
			})
		}
		sizeChanged := false
		if base.Bytes == 0 {
			sizeChanged = row.Bytes != 0
		} else {
			sizeChanged = math.Abs(float64(row.Bytes-base.Bytes))/float64(base.Bytes) > 0.10
		}
		if sizeChanged {
			out = append(out, FuzzAnomaly{
				Seq:  row.Seq,
				Rule: fuzzRuleSize,
				Why:  fmt.Sprintf("bytes %d, baseline %d", row.Bytes, base.Bytes),
			})
		}
		if base.MS > 0 && row.MS > 0 {
			ratio := float64(row.MS) / float64(base.MS)
			delta := row.MS - base.MS
			if delta < 0 {
				delta = -delta
			}
			if (ratio > 3 || ratio < 1.0/3.0) && delta >= base.timingSpread() {
				out = append(out, FuzzAnomaly{
					Seq:  row.Seq,
					Rule: fuzzRuleTime,
					Why: fmt.Sprintf(
						"ms %d, baseline %d (spread %d)",
						row.MS,
						base.MS,
						base.timingSpread(),
					),
				})
			}
		}
	}
	return out
}

// fuzzNeedsBody reports whether any point injects into the request body.
func fuzzNeedsBody(points []FuzzPoint) bool {
	for _, point := range points {
		if point.Location == FuzzBody {
			return true
		}
	}
	return false
}

// fuzzCapturedBody fetches the captured request body once for body injection
// points under a bounded context. It refuses a method that cannot carry a body,
// a captured request with none, and a body the browser no longer retains.
func (p *Page) fuzzCapturedBody(ctx context.Context, entry NetworkEntry) (string, error) {
	method, err := replayMethod(entry.Method, "")
	if err != nil {
		return "", err
	}
	if method == http.MethodGet || method == http.MethodHead || !replayCarriedBody(entry) {
		return "", fmt.Errorf(
			"replay_fuzz: a body injection point needs a captured request body, but %s %s carries none — use a query, header or cookie point",
			method,
			entry.URL,
		)
	}
	bodyCtx, cancel := context.WithTimeout(ctx, fuzzRequestTimeout)
	defer cancel()
	body, retained, err := p.requestPostData(bodyCtx, entry)
	if err != nil {
		return "", fmt.Errorf("replay_fuzz: read captured request body: %w", err)
	}
	if !retained {
		return "", fmt.Errorf(
			"replay_fuzz: the browser no longer retains the captured request body — resend the request or use a query, header or cookie point",
		)
	}
	return body, nil
}

// fuzzInjectBody sets param to value inside body, keeping the captured encoding:
// a JSON object stays JSON and a form body stays form-encoded. A JSON array or
// an unparseable body is refused.
func fuzzInjectBody(body, param, value, contentType string) (string, error) {
	trimmed := strings.TrimSpace(body)
	jsonBody := strings.Contains(strings.ToLower(contentType), "json") ||
		strings.HasPrefix(trimmed, "{") ||
		strings.HasPrefix(trimmed, "[")
	if jsonBody {
		doc := map[string]any{}
		if trimmed != "" {
			if err := json.Unmarshal([]byte(body), &doc); err != nil {
				return "", fmt.Errorf("replay_fuzz: cannot inject %q into a JSON body: %w", param, err)
			}
		}
		doc[param] = value
		out, err := json.Marshal(doc)
		if err != nil {
			return "", fmt.Errorf("replay_fuzz: encode injected JSON body: %w", err)
		}
		return string(out), nil
	}
	form, err := url.ParseQuery(body)
	if err != nil {
		return "", fmt.Errorf("replay_fuzz: cannot parse the request body as a form: %w", err)
	}
	form.Set(param, value)
	return form.Encode(), nil
}
