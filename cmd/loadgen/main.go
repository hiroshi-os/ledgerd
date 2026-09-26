package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type stats struct {
	mu   sync.Mutex
	durs []time.Duration
}

func (s *stats) add(d time.Duration) {
	s.mu.Lock()
	s.durs = append(s.durs, d)
	s.mu.Unlock()
}

func (s *stats) report() (n int, p50, p95, p99 time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n = len(s.durs)
	if n == 0 {
		return
	}
	sorted := append([]time.Duration(nil), s.durs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	p50 = sorted[percentileIndex(n, 50)]
	p95 = sorted[percentileIndex(n, 95)]
	p99 = sorted[percentileIndex(n, 99)]
	return
}

func percentileIndex(n, p int) int {
	idx := (p * n / 100) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return idx
}

func main() {
	base := flag.String("base", "http://127.0.0.1:8080", "API base URL")
	token := flag.String("token", "sk_test_ledgerd", "API key")
	concurrency := flag.Int("c", 32, "concurrency")
	requests := flag.Int("n", 500, "confirm+capture pairs")
	replayN := flag.Int("replay", 200, "idempotent replay samples")
	flag.Parse()

	client := &http.Client{Timeout: 30 * time.Second}
	accountID, err := createAccount(client, *base, *token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create account: %v\n", err)
		os.Exit(1)
	}

	type job struct{ i int }
	jobs := make(chan job, *requests)
	var wg sync.WaitGroup
	var okCount atomic.Int64
	var errCount atomic.Int64
	st := &stats{}

	start := time.Now()
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if err := confirmCapture(client, *base, *token, accountID, j.i, st); err != nil {
					errCount.Add(1)
					fmt.Fprintf(os.Stderr, "job %d: %v\n", j.i, err)
					continue
				}
				okCount.Add(1)
			}
		}()
	}
	for i := 0; i < *requests; i++ {
		jobs <- job{i: i}
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(start)

	n, p50, p95, p99 := st.report()
	throughput := float64(okCount.Load()) / elapsed.Seconds()

	// Measure idempotent replay latency on a fixed intent confirm key.
	piID, confirmKey, err := createProcessingIntent(client, *base, *token, accountID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "setup replay: %v\n", err)
		os.Exit(1)
	}
	replayStats := &stats{}
	for i := 0; i < *replayN; i++ {
		t0 := time.Now()
		if err := postJSON(client, *base+"/v1/payment_intents/"+piID+"/confirm", *token, confirmKey, map[string]any{}); err != nil {
			fmt.Fprintf(os.Stderr, "replay: %v\n", err)
			os.Exit(1)
		}
		replayStats.add(time.Since(t0))
	}
	_, rp50, rp95, rp99 := replayStats.report()

	fmt.Printf("ok=%d err=%d elapsed=%s throughput_confirm_capture_per_s=%.2f samples=%d\n",
		okCount.Load(), errCount.Load(), elapsed, throughput, n)
	fmt.Printf("confirm_capture_latency_ms p50=%.2f p95=%.2f p99=%.2f\n",
		ms(p50), ms(p95), ms(p99))
	fmt.Printf("idempotent_replay_latency_ms p50=%.2f p95=%.2f p99=%.2f samples=%d\n",
		ms(rp50), ms(rp95), ms(rp99), *replayN)
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

func createAccount(client *http.Client, base, token string) (string, error) {
	key := "loadgen-acct-" + uuid.NewString()
	body := map[string]any{"currency": "usd"}
	raw, status, err := doJSON(client, http.MethodPost, base+"/v1/accounts", token, key, body)
	if err != nil {
		return "", err
	}
	if status != 201 {
		return "", fmt.Errorf("status %d: %s", status, raw)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m["id"].(string), nil
}

func confirmCapture(client *http.Client, base, token, accountID string, i int, st *stats) error {
	t0 := time.Now()
	piKey := fmt.Sprintf("lg-pi-%d-%s", i, uuid.NewString())
	body := map[string]any{
		"account_id":       accountID,
		"amount":           100 + (i % 50),
		"currency":         "usd",
		"processor_script": []string{"succeed"},
	}
	raw, status, err := doJSON(client, http.MethodPost, base+"/v1/payment_intents", token, piKey, body)
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("create pi %d: %s", status, raw)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	piID := m["id"].(string)

	if err := postJSON(client, base+"/v1/payment_intents/"+piID+"/confirm", token, "lg-c-"+piID, map[string]any{}); err != nil {
		return err
	}
	if err := postJSON(client, base+"/v1/payment_intents/"+piID+"/capture", token, "lg-cap-"+piID, map[string]any{}); err != nil {
		return err
	}
	st.add(time.Since(t0))
	return nil
}

func createProcessingIntent(client *http.Client, base, token, accountID string) (piID, confirmKey string, err error) {
	piKey := "lg-replay-pi-" + uuid.NewString()
	raw, status, err := doJSON(client, http.MethodPost, base+"/v1/payment_intents", token, piKey, map[string]any{
		"account_id": accountID, "amount": 2500, "currency": "usd", "processor_script": []string{"succeed"},
	})
	if err != nil {
		return "", "", err
	}
	if status != 201 {
		return "", "", fmt.Errorf("status %d: %s", status, raw)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	piID = m["id"].(string)
	confirmKey = "lg-replay-confirm-" + piID
	if err := postJSON(client, base+"/v1/payment_intents/"+piID+"/confirm", token, confirmKey, map[string]any{}); err != nil {
		return "", "", err
	}
	return piID, confirmKey, nil
}

func postJSON(client *http.Client, url, token, idemKey string, body any) error {
	raw, status, err := doJSON(client, http.MethodPost, url, token, idemKey, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("status %d: %s", status, raw)
	}
	return nil
}

func doJSON(client *http.Client, method, url, token, idemKey string, body any) ([]byte, int, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, bytes.NewReader(b))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	return raw, res.StatusCode, err
}
