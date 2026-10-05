package admin

import "encoding/json"

func jsonMarshal(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}

func jsonUnmarshal(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}
