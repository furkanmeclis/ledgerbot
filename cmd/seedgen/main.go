// Command seedgen writes N synthetic companies for load tests:
//
//	go run ./cmd/seedgen -n 4000 -out loadtest/seed
//
// Company i is "load<i>" with API key "lb_load<i>_loadtestsecret000000000000" and
// users "owner" / "accountant" on the dev channel.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	n := flag.Int("n", 4000, "number of companies")
	out := flag.String("out", "loadtest/seed", "output directory")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil { //nolint:gosec // read by the non-root container user
		panic(err)
	}
	for i := 1; i <= *n; i++ {
		id := fmt.Sprintf("load%04d", i)
		key := "lb_" + id + "_loadtestsecret000000000000"
		sum := sha256.Sum256([]byte(key))
		doc := map[string]any{
			"settings": map[string]any{
				"version": 1, "save_mode": "auto_save", "locale": "tr-TR", "ai_daily_limit": 0,
				"api_keys": []map[string]any{{"id": "load", "sha256": hex.EncodeToString(sum[:]), "scopes": []string{"*"}}},
				"rules":    []map[string]any{{"event": "stock.low", "notify_roles": []string{"owner"}, "throttle": "1h"}},
			},
			"permissions": map[string]any{
				"version": 1,
				"roles": map[string]any{
					"owner":      map[string]any{"capabilities": []string{"*"}},
					"accountant": map[string]any{"capabilities": []string{"*"}, "save_mode": "ask_approval"},
				},
				"users": []map[string]any{
					{"id": "u-owner", "role": "owner", "addresses": map[string]string{"dev": "owner"}},
					{"id": "u-acc", "role": "accountant", "addresses": map[string]string{"dev": "accountant"}},
				},
			},
		}
		b, _ := json.Marshal(doc)
		if err := os.WriteFile(filepath.Join(*out, id+".json"), b, 0o644); err != nil { //nolint:gosec // read by the non-root container user
			panic(err)
		}
	}
	fmt.Printf("wrote %d companies to %s\n", *n, *out)
}
