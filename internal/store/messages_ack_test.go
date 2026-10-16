package store_test

import (
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

// Fetch without mark_read, then ack: a fetch whose response was lost leaves
// the messages unread for the next fetch.
func TestFetchThenAckDeliversAtLeastOnce(t *testing.T) {
	e := newEnv(t)
	reg(t, e, "a", "sa")
	reg(t, e, "b", "sb")
	for _, body := range []string{"one", "two"} {
		if _, err := e.st.SendMessage(bg, "b", api.SendMessageRequest{FromSession: "sb", ToSession: "sa", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	lost, err := e.st.Inbox(bg, "a", "sa", false) // response "lost"
	if err != nil || len(lost) != 2 {
		t.Fatalf("%v %v", lost, err)
	}
	again, _ := e.st.Inbox(bg, "a", "sa", false)
	if len(again) != 2 {
		t.Fatalf("unacked messages vanished: %v", again)
	}
	n, err := e.st.AckMessages(bg, "a", api.AckMessagesRequest{Session: "sa", IDs: []int64{again[0].ID, again[1].ID, 9999}})
	if err != nil || n != 2 {
		t.Fatalf("ack: %d %v", n, err)
	}
	if left, _ := e.st.Inbox(bg, "a", "sa", false); len(left) != 0 {
		t.Fatalf("acked messages still unread: %v", left)
	}
	if n, err = e.st.AckMessages(bg, "a", api.AckMessagesRequest{Session: "sa", IDs: []int64{again[0].ID}}); err != nil || n != 0 {
		t.Fatalf("re-ack: %d %v", n, err)
	}
}

func TestAckOnlyOwnHostAndOwnSession(t *testing.T) {
	e := newEnv(t)
	reg(t, e, "a", "sa")
	reg(t, e, "b", "sb")
	m, err := e.st.SendMessage(bg, "a", api.SendMessageRequest{FromSession: "sa", ToSession: "sb", Body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.st.AckMessages(bg, "a", api.AckMessagesRequest{Session: "sb", IDs: []int64{m.ID}})
	wantErr(t, err, store.ErrSessionOwner)
	// Host a acking its own session cannot touch a message addressed to sb.
	if n, err := e.st.AckMessages(bg, "a", api.AckMessagesRequest{Session: "sa", IDs: []int64{m.ID}}); err != nil || n != 0 {
		t.Fatalf("cross-session ack: %d %v", n, err)
	}
	if _, err := e.st.AckMessages(bg, "a", api.AckMessagesRequest{Session: "sa"}); !api.IsValidation(err) {
		t.Fatalf("empty ids: %v", err)
	}
}
