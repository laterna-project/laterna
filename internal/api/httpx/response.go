package httpx

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// ContentTypeJSON is the content type of JSON responses.
const ContentTypeJSON = "application/json; charset=utf-8"

// WriteJSON serializes v and writes it with the given status. Serialization happens before anything
// is written, so an encoding error gives a real 500 and not a truncated response.
func WriteJSON(w http.ResponseWriter, status int, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", ContentTypeJSON)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, err = w.Write(body)
	return err
}
