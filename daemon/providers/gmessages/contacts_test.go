package gmessages

import (
	"testing"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// A picked contact's ID goes straight into StartChat, which starts a chat
// with a phone number. It must be the number, never Google's participant ID.
func TestContactIDIsThePhoneNumber(t *testing.T) {
	contacts := []*gmproto.Contact{
		{ParticipantID: "238", Name: "Ada Lovelace", Number: &gmproto.ContactNumber{Number: "+15551112222", FormattedNumber: ptr("(555) 111-2222")}},
		{ParticipantID: "9", Name: "", Number: &gmproto.ContactNumber{Number: "+15553334444", FormattedNumber: ptr("(555) 333-4444")}},
		{ParticipantID: "10", Name: "No number"},
	}
	got := matchContacts(contacts, "")
	if len(got) != 2 {
		t.Fatalf("results = %+v; want the two with numbers", got)
	}
	if got[0].ID != "+15551112222" || got[0].Name != "Ada Lovelace" {
		t.Errorf("first = %+v; want ID = the phone number", got[0])
	}
	if got[1].ID != "+15553334444" || got[1].Name != "(555) 333-4444" {
		t.Errorf("unnamed = %+v; want the formatted number as its name", got[1])
	}
	if r := matchContacts(contacts, "ada"); len(r) != 1 || r[0].ID != "+15551112222" {
		t.Errorf("search ada = %+v", r)
	}
	if r := matchContacts(contacts, "3334"); len(r) != 1 || r[0].ID != "+15553334444" {
		t.Errorf("search by number = %+v", r)
	}
}

func ptr(s string) *string { return &s }
