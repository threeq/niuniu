package mcpclient

import (
	"encoding/json"
	"net/http"
)

func decodeBody(r *http.Request, out any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(out)
}
