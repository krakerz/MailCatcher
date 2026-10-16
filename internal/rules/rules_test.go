package rules

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mailcatcher/internal/address"
	"mailcatcher/internal/store"
)

var inboxes = []*store.Inbox{
	{ID: 1, Slug: "default", FormatTemplate: "{local}.{name}", AssumedTLD: "com", IsDefault: true},
	{ID: 2, Slug: "team1", FormatTemplate: "{inbox}-{local}={domain}", Priority: 1},
	{ID: 3, Slug: "team2", FormatTemplate: "{inbox}-{local}={domain}", Priority: 2},
	{ID: 4, Slug: "wide", FormatTemplate: "{local}={domain}", Priority: 5}, // would also match team1-… addresses
	{ID: 5, Slug: "other", Domain: "other.test", FormatTemplate: "{local}+{domain}"},
	{ID: 6, Slug: "pinned", FormatTemplate: "{local}.{name}", AssumedTLD: "com", Addresses: []string{"b.gmail@main.test", "qa-*@MAIN.test"}},
	{ID: 7, Slug: "pinned2", FormatTemplate: "{local}.{name}", AssumedTLD: "com", Addresses: []string{"team1-b=gmail.com@main.test"}, Priority: 9},
}

var addrs = []string{
	"b.gmail@main.test", "team1-b=gmail.com@main.test", "TEAM2-x=yahoo.co.id@main.test",
	"b=gmail.com@main.test", "plain@main.test", "a+b.com@other.test", "a.gmail@other.test",
	"x@unknown.test", "team3-b=gmail.com@main.test", "qa-1@main.test", "a.gmail@main.test",
}

// One rule per inbox: its matches must equal address.Route for every address.
func TestInboxRulesAgreeWithRoute(t *testing.T) {
	var list []*store.ForwardRule
	for _, in := range inboxes {
		list = append(list, &store.ForwardRule{InboxID: in.ID, Destination: in.Slug + "@dest.test", Enabled: true})
	}
	compiled := Compile(list, inboxes, "main.test")
	for _, a := range addrs {
		want, _ := address.Route(inboxes, "main.test", a)
		got := Match(compiled, a)
		var slug string
		for _, in := range inboxes {
			if in.ID == want {
				slug = in.Slug
			}
		}
		if len(got) != 1 || got[0] != slug+"@dest.test" {
			t.Errorf("%s: rules say %v, Route says inbox %s", a, got, slug)
		}
	}
}

func TestGlobAndInboxCombined(t *testing.T) {
	list := []*store.ForwardRule{
		{InboxID: 2, AddressGlob: "team1-*=gmail.com@*", Destination: "A@dest.test", Enabled: true},
		{AddressGlob: "*", Destination: "all@dest.test", Enabled: true},
		{AddressGlob: "plain@main.test", Destination: "all@dest.test", Enabled: true}, // duplicate destination
		{AddressGlob: "*", Destination: "off@dest.test", Enabled: false},
		{InboxID: 99, Destination: "gone@dest.test", Enabled: true},
	}
	compiled := Compile(list, inboxes, "main.test")
	if len(compiled) != 3 {
		t.Fatalf("compiled %d rules, want 3 (disabled and missing-inbox dropped)", len(compiled))
	}
	cases := map[string][]string{
		"team1-c=gmail.com@main.test": {"a@dest.test", "all@dest.test"}, // team1-b=… is pinned to inbox 7
		"team1-b=yahoo.com@main.test": {"all@dest.test"},
		"plain@main.test":             {"all@dest.test"},
	}
	for a, want := range cases {
		if got := Match(compiled, a); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", a, got, want)
		}
	}
}

func TestGlobRegex(t *testing.T) {
	for glob, want := range map[string]string{"": "", "*": "", "b.gmail@x.test": `^b\.gmail@x\.test$`, "*@x.test": `^.*@x\.test$`} {
		if got := GlobRegex(glob); got != want {
			t.Errorf("GlobRegex(%q) = %q, want %q", glob, got, want)
		}
	}
}

// The Worker's JavaScript matcher must agree with Match on the same compiled rules.
func TestWorkerMatcherAgrees(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	list := []*store.ForwardRule{
		{InboxID: 1, Destination: "d1@dest.test", Enabled: true},
		{InboxID: 2, AddressGlob: "*gmail*", Destination: "d2@dest.test", Enabled: true},
		{InboxID: 4, Destination: "d4@dest.test", Enabled: true},
		{InboxID: 5, Destination: "d5@dest.test", Enabled: true},
		{InboxID: 6, Destination: "d6@dest.test", Enabled: true},
		{InboxID: 7, AddressGlob: "team1-*", Destination: "d7@dest.test", Enabled: true},
		{AddressGlob: "plain@*", Destination: "d8@dest.test", Enabled: true},
	}
	compiled := Compile(list, inboxes, "main.test")
	in, _ := json.Marshal(map[string]any{"rules": compiled, "addrs": addrs})

	rulesJS, _ := filepath.Abs("../../worker/src/rules.js")
	script := filepath.Join(t.TempDir(), "check.mjs")
	os.WriteFile(script, []byte(`import { matchRules } from "file://`+rulesJS+`";
let s = ""; for await (const c of process.stdin) s += c;
const { rules, addrs } = JSON.parse(s);
console.log(JSON.stringify(addrs.map((a) => matchRules(rules, a))));`), 0o600)
	cmd := exec.Command(node, script)
	cmd.Stdin = strings.NewReader(string(in))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var js [][]string
	if err := json.Unmarshal(out, &js); err != nil {
		t.Fatalf("node output %q: %v", out, err)
	}
	for i, a := range addrs {
		goRes := Match(compiled, a)
		if goRes == nil {
			goRes = []string{}
		}
		if !reflect.DeepEqual(goRes, js[i]) {
			t.Errorf("%s: Go %v, Worker %v", a, goRes, js[i])
		}
	}
}

// Rule addresses are real recipients, decoded per inbox: the scenario from the
// feature request, with overlapping rules collapsing duplicate destinations.
func TestRealAddressRules(t *testing.T) {
	boxes := []*store.Inbox{
		{ID: 1, Name: "Default", Slug: "default", FormatTemplate: "{local}~{domain}", IsDefault: true},
		{ID: 2, Name: "inb1", Slug: "team1", FormatTemplate: "{inbox}.{local}~{domain}"},
		{ID: 3, Name: "dotcom", Slug: "dc", FormatTemplate: "{local}.{name}", AssumedTLD: "com", Addresses: []string{"pinned@main.test"}},
	}
	list := []*store.ForwardRule{
		{AddressGlob: "Someone@Live.com", Destination: "x@dest.test", Enabled: true},             // any inbox
		{InboxID: 2, AddressGlob: "someone@live.com", Destination: "x@dest.test", Enabled: true}, // overlaps: same target
		{InboxID: 2, AddressGlob: "someone@live.com", Destination: "y@dest.test", Enabled: true},
		{InboxID: 2, AddressGlob: "*@example.org", Destination: "z@dest.test", Enabled: true}, // wildcard local part
		{AddressGlob: "team1.*@main.test", Destination: "p@dest.test", Enabled: true},         // catch-all pattern (our domain)
	}
	compiled := Compile(list, boxes, "main.test")
	cases := map[string][]string{
		"team1.someone~live.com@main.test": {"x@dest.test", "y@dest.test", "p@dest.test"},
		"someone~live.com@main.test":       {"x@dest.test"},
		"team1.other~live.com@main.test":   {"p@dest.test"},
		"team1.qa~example.org@main.test":   {"z@dest.test", "p@dest.test"},
		"qa~example.org@main.test":         nil, // Default: the wildcard rule is inb1-only
		"someone~live.com@other.test":      nil,
	}
	for rcpt, want := range cases {
		if got := Match(compiled, rcpt); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", rcpt, got, want)
		}
	}
	// "dotcom" could encode a@live.com, but its address list only takes pinned@….
	if r := Reach(&store.ForwardRule{AddressGlob: "a@live.com"}, boxes, "main.test"); !reflect.DeepEqual(r, []string{"Default", "inb1"}) {
		t.Errorf("reach: %v", r)
	}
	if r := Reach(&store.ForwardRule{InboxID: 3, AddressGlob: "a@live.com"}, boxes, "main.test"); len(r) != 0 {
		t.Errorf("unreachable rule reported as reaching %v", r)
	}
	assertWorkerAgrees(t, compiled, []string{"team1.someone~live.com@main.test", "someone~live.com@main.test", "team1.qa~example.org@main.test", "qa~example.org@main.test"})
}

func assertWorkerAgrees(t *testing.T, compiled []WorkerRule, rcpts []string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	in, _ := json.Marshal(map[string]any{"rules": compiled, "addrs": rcpts})
	rulesJS, _ := filepath.Abs("../../worker/src/rules.js")
	script := filepath.Join(t.TempDir(), "check.mjs")
	os.WriteFile(script, []byte(`import { matchRules } from "file://`+rulesJS+`";
let s = ""; for await (const c of process.stdin) s += c;
const { rules, addrs } = JSON.parse(s);
console.log(JSON.stringify(addrs.map((a) => matchRules(rules, a))));`), 0o600)
	cmd := exec.Command(node, script)
	cmd.Stdin = strings.NewReader(string(in))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var js [][]string
	json.Unmarshal(out, &js)
	for i, a := range rcpts {
		goRes := Match(compiled, a)
		if goRes == nil {
			goRes = []string{}
		}
		if !reflect.DeepEqual(goRes, js[i]) {
			t.Errorf("%s: Go %v, Worker %v", a, goRes, js[i])
		}
	}
}
