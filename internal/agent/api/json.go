package api

import (
	"encoding/json"
	"io"
	"net/http"
)

func jsonDecodeLimited(res *http.Response, dst any) error {
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(dst)
}
