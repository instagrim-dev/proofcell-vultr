package githubapp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
)

// verifyExactProration independently checks an exact, integral-cent example.
// Non-integral cases are rejected because no rounding rule is specified.
func verifyExactProration(data []byte) error {
	var x struct {
		Paid   json.Number `json:"paid_cents"`
		Days   json.Number `json:"period_days"`
		Unused json.Number `json:"unused_days"`
		Refund json.Number `json:"proposed_refund_cents"`
		Status string      `json:"refund_status"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if d.Decode(&x) != nil {
		return fmt.Errorf("invalid proration data")
	}
	vals := []json.Number{x.Paid, x.Days, x.Unused, x.Refund}
	n := make([]*big.Int, 4)
	for i, v := range vals {
		var ok bool
		n[i], ok = new(big.Int).SetString(string(v), 10)
		if !ok || n[i].Sign() < 0 {
			return fmt.Errorf("invalid proration value")
		}
	}
	if n[1].Sign() == 0 || n[2].Cmp(n[1]) > 0 || x.Status != "proposed" {
		return fmt.Errorf("invalid proration period or status")
	}
	left := new(big.Int).Mul(n[0], n[2])
	right := new(big.Int).Mul(n[3], n[1])
	if left.Cmp(right) != 0 {
		return fmt.Errorf("proposed refund is not exact proration")
	}
	return nil
}
