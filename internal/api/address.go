package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"mailcatcher/internal/address"
	"mailcatcher/internal/store"
)

// MaxAddresses caps one encode/decode call.
const MaxAddresses = 1000

type addressResult struct {
	Input  string `json:"input"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// convert answers POST /api/address/encode and /decode for the key's inbox:
// {"address": "..."} or {"addresses": [...]} in, one result per input out.
func (a *API) convert(encode bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Address   string   `json:"address"`
			Addresses []string `json:"addresses"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			jsonErr(w, http.StatusBadRequest, `body must be JSON: {"addresses": ["..."]} or {"address": "..."}`)
			return
		}
		if req.Address != "" {
			req.Addresses = append([]string{req.Address}, req.Addresses...)
		}
		if len(req.Addresses) == 0 || len(req.Addresses) > MaxAddresses {
			jsonErr(w, http.StatusBadRequest, "send 1 to 1000 addresses")
			return
		}
		in := inbox(r)
		inboxes, err := a.Store.ListInboxes(r.Context())
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		format, err := address.For(in)
		if err != nil {
			jsonErr(w, http.StatusConflict, "this inbox's address format is invalid: "+err.Error())
			return
		}
		out := make([]addressResult, len(req.Addresses))
		for i, addr := range req.Addresses {
			res := addressResult{Input: addr}
			if encode {
				res.Output, res.Error = a.encode(in, inboxes, format, addr)
			} else if id, real := address.Route(inboxes, a.Domain, strings.TrimSpace(addr)); id == in.ID && real != "" {
				res.Output = real
			} else {
				res.Error = "not an address of this inbox"
			}
			out[i] = res
		}
		writeJSON(w, map[string]any{"results": out})
	}
}

// encode also checks that mail to the result really lands in this inbox and
// decodes back, since another inbox's address list or format may claim it first.
func (a *API) encode(in *store.Inbox, inboxes []*store.Inbox, f *address.Format, real string) (string, string) {
	local, err := f.Encode(real)
	if err != nil {
		return "", err.Error()
	}
	enc := local + "@" + address.CatchDomain(in, a.Domain)
	if id, back := address.Route(inboxes, a.Domain, enc); id != in.ID || back != strings.ToLower(strings.TrimSpace(real)) {
		return "", "mail to the encoded address would not reach this inbox"
	}
	return enc, ""
}
