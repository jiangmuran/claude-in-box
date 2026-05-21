package server

import "encoding/json"

// Centralized so handlers stay terse and reviewers can find every JSON
// touchpoint in one file.
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
