package whatsapp

import (
	"strings"
	"testing"

	"omarchy-omamessages/core"
)

func TestParseTarget(t *testing.T) {
	for in, want := range map[string]string{
		"15551112222@s.whatsapp.net":   "15551112222@s.whatsapp.net",
		"15551112222:7@s.whatsapp.net": "15551112222@s.whatsapp.net", // device suffix dropped
		"120363000000000001@g.us":      "120363000000000001@g.us",
	} {
		tg, err := parseTarget(in)
		if err != nil || tg.jid.String() != want || tg.phone != "" {
			t.Errorf("parseTarget(%q) = %+v, %v; want jid %s", in, tg, err, want)
		}
	}
	tg, err := parseTarget(" +1 (555) 010-2030 ")
	if err != nil || tg.phone != "+15550102030" || !tg.jid.IsEmpty() {
		t.Errorf("phone target = %+v, %v; want +15550102030 to look up", tg, err)
	}
	for _, bad := range []string{"", "Ada", "123", "@s.whatsapp.net"} {
		if _, err := parseTarget(bad); err == nil {
			t.Errorf("parseTarget(%q) accepted", bad)
		}
	}
}

func TestMatchContacts(t *testing.T) {
	people := []core.Participant{
		{ID: "1", Name: "Zoe Adams", Number: "+15551110001"},
		{ID: "2", Name: "Adam Smith", Number: "+15551110002"},
		{ID: "3", Name: "Grace Hopper", Number: "+44207946000"},
		{ID: "4", Name: "Book club", Number: "group"},
		{ID: "5", Name: "ada", Number: "+15551110005"},
	}
	names := func(ps []core.Participant) string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Name)
		}
		return strings.Join(out, ", ")
	}
	// Starts-with first, then contains; alphabetical, case-insensitive.
	if got := names(matchContacts(people, "ada", 20)); got != "ada, Adam Smith, Zoe Adams" {
		t.Errorf("ada → %s", got)
	}
	if got := names(matchContacts(people, "2079", 20)); got != "Grace Hopper" {
		t.Errorf("number search → %s", got)
	}
	if got := names(matchContacts(people, "12", 20)); got != "" {
		t.Errorf("two digits matched numbers: %s", got)
	}
	if got := names(matchContacts(people, "club", 20)); got != "Book club" {
		t.Errorf("group → %s", got)
	}
	if got := matchContacts(people, "", 2); len(got) != 2 {
		t.Errorf("limit not applied: %d", len(got))
	}
}
