package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"mailcatcher/internal/auth"
	"mailcatcher/internal/store"
)

func post(t *testing.T, url, k, body string) (int, []addressResult) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, bytes.NewBufferString(body))
	if k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct{ Results []addressResult }
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out.Results
}

func TestAddressEncodeDecode(t *testing.T) {
	srv, st, _ := setup(t)
	ctx := context.Background()
	in := &store.Inbox{Name: "Selly", Slug: "selly", FormatTemplate: "{local}={domain}", AssumedTLD: "com"}
	if err := st.CreateInbox(ctx, in); err != nil {
		t.Fatal(err)
	}
	const sellyKey = "mc_selly-key"
	if err := st.SetInboxAPIKeyHash(ctx, in.ID, auth.HashToken(sellyKey)); err != nil {
		t.Fatal(err)
	}

	code, res := post(t, srv.URL+"/api/address/encode", sellyKey, `{"addresses": ["A@Gmail.com", "nope"]}`)
	if code != 200 || len(res) != 2 || res[0].Output != "a=gmail.com@example.test" || res[0].Error != "" || res[1].Error == "" || res[1].Output != "" {
		t.Fatalf("encode batch: %d %+v", code, res)
	}
	code, res = post(t, srv.URL+"/api/address/decode", sellyKey, `{"address": "a=gmail.com@example.test"}`)
	if code != 200 || len(res) != 1 || res[0].Output != "a@gmail.com" {
		t.Fatalf("decode single: %d %+v", code, res)
	}
	// Wrong domain, not the format, and another inbox's key all get a per-item error.
	_, res = post(t, srv.URL+"/api/address/decode", sellyKey, `{"addresses": ["a=gmail.com@other.test", "plain@example.test"]}`)
	if len(res) != 2 || res[0].Error == "" || res[1].Error == "" {
		t.Fatalf("decode foreign: %+v", res)
	}
	if _, res = post(t, srv.URL+"/api/address/decode", key, `{"address": "a=gmail.com@example.test"}`); len(res) != 1 || res[0].Error == "" || res[0].Output != "" {
		t.Fatalf("default inbox key decoded selly's address: %+v", res)
	}

	if code, _ = post(t, srv.URL+"/api/address/encode", "", `{"address": "a@gmail.com"}`); code != http.StatusUnauthorized {
		t.Errorf("no key: %d", code)
	}
	if code, _ = post(t, srv.URL+"/api/address/encode", sellyKey, `{"addresses": []}`); code != http.StatusBadRequest {
		t.Errorf("empty list: %d", code)
	}
	if code, _ = post(t, srv.URL+"/api/address/encode", sellyKey, `not json`); code != http.StatusBadRequest {
		t.Errorf("bad body: %d", code)
	}
}
