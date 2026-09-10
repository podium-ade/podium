package local

import "net/http"

// bearer presents the shared dev token on every request. Under the local transport an
// operator and a node authenticate the same way; a node additionally proves possession
// of its node key inside Hello.
type bearer struct {
	rt    http.RoundTripper
	token string
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.rt.RoundTrip(r)
}

// NewClient is the client for unary and server-streaming Connect calls: plain HTTP/1.1,
// which is all the CLI and the web UI need. It has no timeout, because
// StreamTaskEvents follows a task for as long as it runs.
func NewClient(token string) *http.Client {
	return &http.Client{Transport: bearer{rt: http.DefaultTransport, token: token}}
}

// NewStreamClient is the client for NodeService.Stream. Connect refuses a bidirectional
// stream on HTTP/1.1, so this transport speaks cleartext HTTP/2 (h2c) with prior
// knowledge. HTTP/1.1 is deliberately left off: a silent downgrade would surface as a
// confusing runtime failure on the first Stream call rather than a connection error.
func NewStreamClient(token string) *http.Client {
	tr := &http.Transport{Protocols: new(http.Protocols)}
	tr.Protocols.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: bearer{rt: tr, token: token}}
}
