package browsers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
)

func fuzzPoints(n int) []FuzzPoint {
	out := make([]FuzzPoint, n)
	for i := range out {
		out[i] = FuzzPoint{Param: fmt.Sprintf("p%d", i), Location: FuzzQuery}
	}
	return out
}

func fuzzPayloads(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("v%d", i)
	}
	return out
}

func TestFuzzValidateSizing(t *testing.T) {
	tests := []struct {
		name     string
		strategy FuzzStrategy
		points   int
		payloads int
		want     string
	}{
		{name: "sniper multiplies points by payloads", strategy: FuzzSniper, points: 3, payloads: 4, want: "12"},
		{name: "empty strategy defaults to sniper", points: 3, payloads: 4, want: "12"},
		{name: "pitchfork stops at the shorter list", strategy: FuzzPitchfork, points: 3, payloads: 4, want: "3"},
		{name: "pitchfork with more payloads than points", strategy: FuzzPitchfork, points: 2, payloads: 5, want: "2"},
		{name: "clusterbomb raises payloads to points", strategy: FuzzClusterbomb, points: 3, payloads: 4, want: "64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := FuzzSpec{
				Strategy: tt.strategy,
				Points:   fuzzPoints(tt.points),
				Payloads: fuzzPayloads(tt.payloads),
			}
			got, err := fuzzValidate(spec)
			if err != nil {
				t.Fatalf("fuzzValidate() error = %v", err)
			}
			if got.String() != tt.want {
				t.Fatalf("fuzzValidate() size = %s, want %s", got, tt.want)
			}
			plan := fuzzPlan(spec.Strategy, spec.Points, spec.Payloads)
			if len(plan) != int(got.Int64()) {
				t.Fatalf("fuzzPlan() built %d variants, computed size %s", len(plan), got)
			}
		})
	}
}

func TestFuzzValidateRefusal(t *testing.T) {
	clusterbomb := new(big.Int).Exp(big.NewInt(50), big.NewInt(20), nil).String()
	tests := []struct {
		name     string
		spec     FuzzSpec
		unit     string
		computed string
		bound    string
	}{
		{
			name:     "five hundred payloads exceed the request bound",
			spec:     FuzzSpec{Strategy: FuzzSniper, Points: fuzzPoints(1), Payloads: fuzzPayloads(500)},
			unit:     "requests",
			computed: "500",
			bound:    "100",
		},
		{
			name:     "over the request bound",
			spec:     FuzzSpec{Strategy: FuzzSniper, Points: fuzzPoints(3), Payloads: fuzzPayloads(40)},
			unit:     "requests",
			computed: "120",
			bound:    "100",
		},
		{
			name:     "clusterbomb names the exact computed size",
			spec:     FuzzSpec{Strategy: FuzzClusterbomb, Points: fuzzPoints(20), Payloads: fuzzPayloads(50)},
			unit:     "requests",
			computed: clusterbomb,
			bound:    "100",
		},
		{
			name:     "too many points",
			spec:     FuzzSpec{Strategy: FuzzSniper, Points: fuzzPoints(21), Payloads: fuzzPayloads(1)},
			unit:     "injection points",
			computed: "21",
			bound:    "20",
		},
		{
			name:     "too many payloads",
			spec:     FuzzSpec{Strategy: FuzzSniper, Points: fuzzPoints(1), Payloads: fuzzPayloads(51)},
			unit:     "payloads",
			computed: "51",
			bound:    "50",
		},
		{
			name: "payload over the rune cap",
			spec: FuzzSpec{
				Strategy: FuzzSniper,
				Points:   fuzzPoints(1),
				Payloads: []string{strings.Repeat("é", 201)},
			},
			unit:     "payload 1 runes",
			computed: "201",
			bound:    "200",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := fuzzValidate(tt.spec)
			if err == nil {
				t.Fatal("fuzzValidate() = nil error, want a bounds refusal")
			}
			var bounds *FuzzBoundsError
			if !errors.As(err, &bounds) {
				t.Fatalf("fuzzValidate() error = %v, want *FuzzBoundsError", err)
			}
			if bounds.Unit != tt.unit {
				t.Fatalf("fuzzValidate() unit = %q, want %q", bounds.Unit, tt.unit)
			}
			if bounds.Computed != tt.computed {
				t.Fatalf("fuzzValidate() computed = %q, want %q", bounds.Computed, tt.computed)
			}
			if bounds.Bound != tt.bound {
				t.Fatalf("fuzzValidate() bound = %q, want %q", bounds.Bound, tt.bound)
			}
			for _, want := range []string{tt.computed, tt.bound} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("fuzzValidate() error = %q, want it to name %q", err, want)
				}
			}
		})
	}
}

func TestFuzzValidateRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		spec FuzzSpec
		want string
	}{
		{
			name: "unknown strategy",
			spec: FuzzSpec{Strategy: "spray", Points: fuzzPoints(1), Payloads: fuzzPayloads(1)},
			want: "unknown strategy",
		},
		{
			name: "no points",
			spec: FuzzSpec{Strategy: FuzzSniper, Payloads: fuzzPayloads(1)},
			want: "at least one injection point",
		},
		{
			name: "no payloads",
			spec: FuzzSpec{Strategy: FuzzSniper, Points: fuzzPoints(1)},
			want: "at least one payload",
		},
		{
			name: "unknown location",
			spec: FuzzSpec{
				Strategy: FuzzSniper,
				Points:   []FuzzPoint{{Param: "id", Location: "path"}},
				Payloads: fuzzPayloads(1),
			},
			want: "unknown location",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := fuzzValidate(tt.spec)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("fuzzValidate() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestFuzzPlanOrder(t *testing.T) {
	points := []FuzzPoint{
		{Param: "q", Location: FuzzQuery},
		{Param: "r", Location: FuzzQuery},
	}
	payloads := []string{"a", "b"}
	type want struct {
		point   string
		payload string
	}
	tests := []struct {
		name     string
		strategy FuzzStrategy
		want     []want
	}{
		{
			name:     "sniper walks each point across every payload",
			strategy: FuzzSniper,
			want: []want{
				{point: "q", payload: "a"},
				{point: "q", payload: "b"},
				{point: "r", payload: "a"},
				{point: "r", payload: "b"},
			},
		},
		{
			name:     "pitchfork pairs by index",
			strategy: FuzzPitchfork,
			want: []want{
				{point: "q", payload: "a"},
				{point: "r", payload: "b"},
			},
		},
		{
			name:     "clusterbomb covers the product in odometer order",
			strategy: FuzzClusterbomb,
			want: []want{
				{point: "q,r", payload: "a | a"},
				{point: "q,r", payload: "a | b"},
				{point: "q,r", payload: "b | a"},
				{point: "q,r", payload: "b | b"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := fuzzPlan(tt.strategy, points, payloads)
			if len(plan) != len(tt.want) {
				t.Fatalf("fuzzPlan() built %d variants, want %d", len(plan), len(tt.want))
			}
			for i, got := range plan {
				if got.point != tt.want[i].point || got.payload != tt.want[i].payload {
					t.Fatalf(
						"fuzzPlan() variant %d = {%s, %s}, want {%s, %s}",
						i,
						got.point,
						got.payload,
						tt.want[i].point,
						tt.want[i].payload,
					)
				}
			}
		})
	}
}

func TestFuzzAnomalies(t *testing.T) {
	base := FuzzBaseline{Status: 200, Bytes: 1000, ColdMS: 150, MS: 100}
	tests := []struct {
		name string
		row  FuzzRun
		want []string
	}{
		{
			name: "matching row has no anomaly",
			row:  FuzzRun{Seq: 1, Status: 200, Bytes: 1000, MS: 100},
		},
		{
			name: "status change fires",
			row:  FuzzRun{Seq: 2, Status: 500, Bytes: 1000, MS: 100},
			want: []string{fuzzRuleStatus},
		},
		{
			name: "size above ten percent fires",
			row:  FuzzRun{Seq: 3, Status: 200, Bytes: 1201, MS: 100},
			want: []string{fuzzRuleSize},
		},
		{
			name: "size at ten percent does not fire",
			row:  FuzzRun{Seq: 4, Status: 200, Bytes: 1100, MS: 100},
		},
		{
			name: "slower than three times fires",
			row:  FuzzRun{Seq: 5, Status: 200, Bytes: 1000, MS: 350},
			want: []string{fuzzRuleTime},
		},
		{
			name: "faster than a third fires",
			row:  FuzzRun{Seq: 6, Status: 200, Bytes: 1000, MS: 20},
			want: []string{fuzzRuleTime},
		},
		{
			name: "exactly three times does not fire",
			row:  FuzzRun{Seq: 7, Status: 200, Bytes: 1000, MS: 300},
		},
		{
			name: "every rule can fire on one row",
			row:  FuzzRun{Seq: 8, Status: 500, Bytes: 500, MS: 400},
			want: []string{fuzzRuleStatus, fuzzRuleSize, fuzzRuleTime},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fuzzAnomalies([]FuzzRun{tt.row}, base)
			if len(got) != len(tt.want) {
				t.Fatalf("fuzzAnomalies() = %v, want %v", got, tt.want)
			}
			for i, anomaly := range got {
				if anomaly.Seq != tt.row.Seq {
					t.Fatalf("fuzzAnomalies() seq = %d, want %d", anomaly.Seq, tt.row.Seq)
				}
				if anomaly.Rule != tt.want[i] {
					t.Fatalf("fuzzAnomalies() rule = %q, want %q", anomaly.Rule, tt.want[i])
				}
				if anomaly.Why == "" {
					t.Fatal("fuzzAnomalies() why is empty")
				}
			}
		})
	}
}

// TestFuzzWarmBaseline probes the baseline twice, compares against the warm
// sample, and suppresses a time_delta that fits inside the cold-to-warm spread.
func TestFuzzWarmBaseline(t *testing.T) {
	probes := []ReplayResult{
		{Status: 200, BodyBytes: 1000, MS: 2000},
		{Status: 200, BodyBytes: 1000, MS: 300},
	}
	calls := 0
	probe := func() (ReplayResult, error) {
		res := probes[calls]
		calls++
		return res, nil
	}

	base, err := fuzzWarmBaseline(probe)
	if err != nil {
		t.Fatalf("fuzzWarmBaseline() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("fuzzWarmBaseline() probes = %d, want 2", calls)
	}
	if base.ColdMS != 2000 || base.MS != 300 {
		t.Fatalf("baseline cold_ms/ms = %d/%d, want 2000/300", base.ColdMS, base.MS)
	}
	if base.Status != 200 || base.Bytes != 1000 {
		t.Fatalf("baseline status/bytes = %d/%d, want the warm probe's 200/1000", base.Status, base.Bytes)
	}

	inside := fuzzAnomalies([]FuzzRun{{Seq: 1, Status: 200, Bytes: 1000, MS: 1100}}, base)
	if len(inside) != 0 {
		t.Fatalf("fuzzAnomalies() inside the spread = %v, want no time_delta", inside)
	}
	outside := fuzzAnomalies([]FuzzRun{{Seq: 2, Status: 200, Bytes: 1000, MS: 4000}}, base)
	if len(outside) != 1 || outside[0].Rule != fuzzRuleTime {
		t.Fatalf("fuzzAnomalies() beyond the spread = %v, want one time_delta", outside)
	}
}

func TestFuzzExecuteKeepsPlanOrderUnderConcurrency(t *testing.T) {
	const n = 12
	plan := make([]fuzzVariant, n)
	for i := range plan {
		plan[i] = fuzzVariant{point: "p", payload: fmt.Sprintf("v%d", i)}
	}

	var inFlight, peak atomic.Int64
	run := func(ctx context.Context, variant fuzzVariant) (FuzzRun, error) {
		current := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			old := peak.Load()
			if current <= old || peak.CompareAndSwap(old, current) {
				break
			}
		}
		var i int
		if _, err := fmt.Sscanf(variant.payload, "v%d", &i); err != nil {
			return FuzzRun{}, err
		}
		time.Sleep(time.Duration(n-i) * time.Millisecond)
		return FuzzRun{Payload: variant.payload, Point: variant.point, Status: 200}, nil
	}

	rows, err := fuzzExecute(context.Background(), plan, run)
	if err != nil {
		t.Fatalf("fuzzExecute() error = %v", err)
	}
	if len(rows) != n {
		t.Fatalf("fuzzExecute() rows = %d, want %d", len(rows), n)
	}
	for i, row := range rows {
		if row.Seq != i+1 {
			t.Fatalf("fuzzExecute() row %d seq = %d, want %d", i, row.Seq, i+1)
		}
		if want := fmt.Sprintf("v%d", i); row.Payload != want {
			t.Fatalf("fuzzExecute() row %d payload = %q, want %q", i, row.Payload, want)
		}
	}
	if got := peak.Load(); got > fuzzConcurrency {
		t.Fatalf("fuzzExecute() peak in flight = %d, want <= %d", got, fuzzConcurrency)
	}
}

func TestFuzzExecuteReturnsFinishedRowsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	plan := make([]fuzzVariant, 20)
	for i := range plan {
		plan[i] = fuzzVariant{point: "p", payload: fmt.Sprintf("v%d", i)}
	}
	finished := make(chan struct{})
	run := func(ctx context.Context, variant fuzzVariant) (FuzzRun, error) {
		switch variant.payload {
		case "v0":
			close(finished)
			return FuzzRun{Payload: variant.payload, Point: variant.point, Status: 200}, nil
		case "v1":
			<-finished
			cancel()
			return FuzzRun{}, context.Canceled
		default:
			<-ctx.Done()
			return FuzzRun{}, ctx.Err()
		}
	}

	rows, err := fuzzExecute(ctx, plan, run)
	if err == nil {
		t.Fatal("fuzzExecute() error = nil, want the cancellation")
	}
	if len(rows) != 1 {
		t.Fatalf("fuzzExecute() rows = %d, want the one finished row", len(rows))
	}
	if rows[0].Seq != 1 || rows[0].Payload != "v0" {
		t.Fatalf("fuzzExecute() finished row = %+v, want seq 1 payload v0", rows[0])
	}
}

func TestReadReplayBodyMeasuresTrueSize(t *testing.T) {
	tests := []struct {
		name      string
		size      int
		limit     int64
		wantBytes int64
		wantFull  int64
		wantCut   bool
	}{
		{name: "under the cap is not cut", size: 100, limit: 4000, wantBytes: 100, wantFull: 100},
		{name: "at the cap is not cut", size: 4000, limit: 4000, wantBytes: 4000, wantFull: 4000},
		{name: "over the cap reports the true size", size: 50806, limit: 4000, wantBytes: 4000, wantFull: 50806, wantCut: true},
		{name: "one byte over the cap reports the true size", size: 4001, limit: 4000, wantBytes: 4000, wantFull: 4001, wantCut: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(strings.Repeat("a", tt.size)))}
			got, err := readReplayBody(resp, tt.limit)
			if err != nil {
				t.Fatalf("readReplayBody() error = %v", err)
			}
			if got.Bytes != tt.wantBytes {
				t.Fatalf("readReplayBody() bytes = %d, want %d", got.Bytes, tt.wantBytes)
			}
			if got.FullBytes != tt.wantFull {
				t.Fatalf("readReplayBody() full bytes = %d, want %d", got.FullBytes, tt.wantFull)
			}
			if got.Truncated != tt.wantCut {
				t.Fatalf("readReplayBody() truncated = %t, want %t", got.Truncated, tt.wantCut)
			}
		})
	}
}

// TestFuzzCappedMatrixKeepsSizeAnomalies runs a matrix through the real plan,
// concurrency and mapping path with a cap far below every body, then checks the
// true sizes and the three size_delta anomalies still surface.
func TestFuzzCappedMatrixKeepsSizeAnomalies(t *testing.T) {
	const (
		cap          = 4000
		baselineSize = 50806
		smallSize    = 12777
	)
	sizes := map[string]int{
		"v0": smallSize,
		"v1": smallSize,
		"v2": smallSize,
		"v3": baselineSize,
	}
	bodyOf := func(payload string) ReplayResult {
		size, ok := sizes[payload]
		if !ok {
			return ReplayResult{}
		}
		resp := &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("a", size))),
		}
		res, err := replayResult(resp, nil, ReplaySent{}, true, time.Now(), cap)
		if err != nil {
			t.Fatalf("replayResult(%s) error = %v", payload, err)
		}
		return res
	}

	base := fuzzBaselineOf(bodyOf("v3"), bodyOf("v3"))
	if base.Bytes != baselineSize {
		t.Fatalf("baseline bytes = %d, want the true size %d", base.Bytes, baselineSize)
	}

	points := []FuzzPoint{{Param: "q", Location: FuzzQuery}}
	payloads := []string{"v0", "v1", "v2", "v3"}
	plan := fuzzPlan(FuzzSniper, points, payloads)

	rows, err := fuzzExecute(context.Background(), plan, func(_ context.Context, variant fuzzVariant) (FuzzRun, error) {
		return fuzzRunOf(variant, bodyOf(variant.payload)), nil
	})
	if err != nil {
		t.Fatalf("fuzzExecute() error = %v", err)
	}
	if len(rows) != len(payloads) {
		t.Fatalf("fuzzExecute() rows = %d, want %d", len(rows), len(payloads))
	}
	for _, row := range rows {
		want, ok := sizes[row.Payload]
		if !ok {
			t.Fatalf("unexpected payload %q", row.Payload)
		}
		if row.Bytes != int64(want) {
			t.Fatalf("row %d bytes = %d, want the true size %d", row.Seq, row.Bytes, want)
		}
		if !row.BodyCut {
			t.Fatalf("row %d body_cut = false, want true under a %d-byte cap", row.Seq, cap)
		}
	}

	anomalies := fuzzAnomalies(rows, base)
	sizeHits := 0
	for _, anomaly := range anomalies {
		if anomaly.Rule != fuzzRuleSize {
			t.Fatalf("anomaly = %+v, want only size_delta under a cap", anomaly)
		}
		sizeHits++
	}
	if sizeHits != 3 {
		t.Fatalf("size_delta anomalies = %d, want the three true-size rows: %+v", sizeHits, anomalies)
	}
}

func TestReplayTargetErrorsNameTheAction(t *testing.T) {
	page := &Page{observer: newPageObserver()}
	for i, rawURL := range []string{"https://api.test/users/1", "https://api.test/users/2"} {
		page.observer.recordRequest(&proto.NetworkRequestWillBeSent{
			RequestID: proto.NetworkRequestID(fmt.Sprintf("req-%d", i)),
			Type:      proto.NetworkResourceTypeXHR,
			Request:   &proto.NetworkRequest{Method: "GET", URL: rawURL},
			Timestamp: 1,
		})
	}

	_, err := page.ReplayFuzz(context.Background(), FuzzSpec{
		Target:   "/users/",
		Points:   []FuzzPoint{{Param: "q", Location: FuzzQuery}},
		Payloads: []string{"a"},
	})
	if err == nil {
		t.Fatal("ReplayFuzz() error = nil, want the ambiguous target")
	}
	if !errors.Is(err, ErrReplayTargetAmbiguous) {
		t.Fatalf("ReplayFuzz() error = %v, want it to wrap ErrReplayTargetAmbiguous", err)
	}
	if !strings.Contains(err.Error(), "replay_fuzz") {
		t.Fatalf("ReplayFuzz() error = %q, want it to name replay_fuzz", err)
	}
	if !strings.Contains(err.Error(), "target is ambiguous") {
		t.Fatalf("ReplayFuzz() error = %q, want the ambiguous condition", err)
	}

	_, err = page.Replay(context.Background(), ReplaySpec{Target: "/users/"})
	if err == nil {
		t.Fatal("Replay() error = nil, want the ambiguous target")
	}
	if !errors.Is(err, ErrReplayTargetAmbiguous) {
		t.Fatalf("Replay() error = %v, want it to wrap ErrReplayTargetAmbiguous", err)
	}
	if strings.Contains(err.Error(), "replay_fuzz") {
		t.Fatalf("Replay() error = %q, want it not to name replay_fuzz", err)
	}
	if !strings.Contains(err.Error(), "replay: target is ambiguous") {
		t.Fatalf("Replay() error = %q, want it to name replay", err)
	}
}
