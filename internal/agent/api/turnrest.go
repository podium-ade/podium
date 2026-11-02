package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// The turn surface as plain HTTP resources: what a running task calls back to.
//
//	POST   /v1/delegations                  delegate a piece of work   {playbook, instruction, readOnly}
//	GET    /v1/delegations                  this conversation's delegations
//	GET    /v1/delegations/{id}             one, and the last thing its task said
//	POST   /v1/delegations/{id}/cancel      stop it                    {reason}
//	POST   /v1/delegations/{id}/messages    tell its task something    {text}
//	DELETE /v1/delegations/{id}/preview     release its preview
//	POST   /v1/git-tokens                   redeem the turn's capability for a GitHub token
//	       /v1/mcp/pull-request             the pull-request reader (MCP)
//
// Every call carries the turn's credential as `Authorization: Bearer <token>`. Bodies and
// answers are the TurnService and GitCredentialService messages in protobuf's JSON mapping
// (camelCase out, either naming in), and a refusal is {"code", "message"} with the HTTP status
// the code stands for. It is the same implementation the Connect routes reach: this file only
// translates the shape.

// TurnRESTPrefix is where these resources live, on the conductor and under podium-server's
// /turn-surface.
const TurnRESTPrefix = "/v1/"

// maxTurnRESTBody bounds a request body. The largest is an instruction to a delegated task.
const maxTurnRESTBody = 1 << 20

// NewTurnREST returns the resources above. A nil service leaves its routes out; prMCP is the
// pull-request reader, mounted as it is.
func NewTurnREST(turns *TurnService, gitcred *GitCredentialService, prMCP http.Handler) http.Handler {
	mux := http.NewServeMux()
	if turns != nil {
		mux.HandleFunc("POST /v1/delegations", func(w http.ResponseWriter, r *http.Request) {
			in := &agentv1.DelegateRequest{}
			if !readBody(w, r, in) {
				return
			}
			serveTurnREST(w, r, http.StatusCreated, in, turns.Delegate)
		})
		mux.HandleFunc("GET /v1/delegations", func(w http.ResponseWriter, r *http.Request) {
			serveTurnREST(w, r, http.StatusOK, &agentv1.ListDelegationsRequest{}, turns.ListDelegations)
		})
		mux.HandleFunc("GET /v1/delegations/{id}", func(w http.ResponseWriter, r *http.Request) {
			serveTurnREST(w, r, http.StatusOK, &agentv1.GetDelegationRequest{Id: r.PathValue("id")}, turns.GetDelegation)
		})
		mux.HandleFunc("POST /v1/delegations/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
			in := &agentv1.CancelDelegationRequest{}
			if !readBody(w, r, in) {
				return
			}
			in.Id = r.PathValue("id")
			serveTurnREST(w, r, http.StatusOK, in, turns.CancelDelegation)
		})
		mux.HandleFunc("POST /v1/delegations/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
			in := &agentv1.InjectDelegationRequest{}
			if !readBody(w, r, in) {
				return
			}
			in.Id = r.PathValue("id")
			serveTurnREST(w, r, http.StatusOK, in, turns.InjectDelegation)
		})
		mux.HandleFunc("DELETE /v1/delegations/{id}/preview", func(w http.ResponseWriter, r *http.Request) {
			serveTurnREST(w, r, http.StatusOK, &agentv1.ReleasePreviewRequest{Id: r.PathValue("id")}, turns.ReleasePreview)
		})
	}
	if gitcred != nil {
		mux.HandleFunc("POST /v1/git-tokens", func(w http.ResponseWriter, r *http.Request) {
			serveTurnREST(w, r, http.StatusCreated, &agentv1.MintTokenRequest{}, gitcred.MintToken)
		})
	}
	if prMCP != nil {
		mux.Handle(PullRequestMCPRESTPath, prMCP)
	}
	return mux
}

// PullRequestMCPRESTPath is the pull-request reader among the resources.
const PullRequestMCPRESTPath = "/v1/mcp/pull-request"

// readBody decodes an optional JSON body into msg. An empty body is an empty message.
func readBody(w http.ResponseWriter, r *http.Request, msg proto.Message) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxTurnRESTBody))
	if err != nil {
		writeTurnRESTError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("the request body is too large or unreadable")))
		return false
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return true
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, msg); err != nil {
		writeTurnRESTError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("the request body is not the JSON this route takes: "+err.Error())))
		return false
	}
	return true
}

// serveTurnREST calls one service method with the request's bearer as the turn's credential
// and writes its answer.
func serveTurnREST[Req, Res any](
	w http.ResponseWriter, r *http.Request, ok int, in *Req,
	call func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error),
) {
	req := connect.NewRequest(in)
	if token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); found {
		req.Header().Set(TurnTokenHeader, strings.TrimSpace(token))
	}
	res, err := call(r.Context(), req)
	if err != nil {
		writeTurnRESTError(w, err)
		return
	}
	out, err := protojson.Marshal(any(res.Msg).(proto.Message))
	if err != nil {
		writeTurnRESTError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(ok)
	_, _ = w.Write(out)
}

func writeTurnRESTError(w http.ResponseWriter, err error) {
	code := connect.CodeOf(err)
	msg := err.Error()
	var ce *connect.Error
	if errors.As(err, &ce) {
		msg = ce.Message()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatusOf(code))
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code.String(), "message": msg})
}

// httpStatusOf is the HTTP status a Connect code stands for, as Connect's own protocol maps it.
func httpStatusOf(code connect.Code) int {
	switch code {
	case connect.CodeCanceled:
		return 499
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeOutOfRange:
		return http.StatusBadRequest
	case connect.CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	case connect.CodeNotFound:
		return http.StatusNotFound
	case connect.CodeAlreadyExists, connect.CodeAborted:
		return http.StatusConflict
	case connect.CodePermissionDenied:
		return http.StatusForbidden
	case connect.CodeResourceExhausted:
		return http.StatusTooManyRequests
	case connect.CodeUnimplemented:
		return http.StatusNotImplemented
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable
	case connect.CodeUnauthenticated:
		return http.StatusUnauthorized
	default:
		return http.StatusInternalServerError
	}
}
