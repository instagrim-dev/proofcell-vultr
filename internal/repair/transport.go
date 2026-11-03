package repair

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type Request struct {
	Choice   string `json:"choice"`
	Previous []byte `json:"previous"`
}
type Client struct{ Socket string }

func (c Client) Repair(ctx context.Context, choice string, previous []byte) (Result, error) {
	client := &http.Client{Timeout: 220 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	}}}
	r, w := io.Pipe()
	go func() {
		err := json.NewEncoder(w).Encode(Request{Choice: choice, Previous: previous})
		_ = w.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, "POST", "http://unix/repair", r)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Result{}, fmt.Errorf("repair worker: %s", string(body))
	}
	var result Result
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result)
	return result, err
}
func Handler(s *Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/repair" {
			http.NotFound(w, r)
			return
		}
		var input Request
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&input); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		result, err := s.Repair(r.Context(), input.Choice, input.Previous)
		if err != nil {
			http.Error(w, err.Error(), 422)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}
