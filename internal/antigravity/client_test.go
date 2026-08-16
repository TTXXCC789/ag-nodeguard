package antigravity

import "testing"

func TestCompletedSuccessfully(t *testing.T) {
	for _, status := range []string{"CASCADE_RUN_STATUS_IDLE", "complete", "SUCCESS"} {
		if !(Conversation{Status: status}).CompletedSuccessfully() {
			t.Fatalf("%q should be complete", status)
		}
	}
	for _, status := range []string{"CASCADE_RUN_STATUS_RUNNING", "CASCADE_RUN_STATUS_BUSY", ""} {
		if (Conversation{Status: status}).CompletedSuccessfully() {
			t.Fatalf("%q should not be complete", status)
		}
	}
}

func TestConversationIsRunning(t *testing.T) {
	for _, status := range []string{"CASCADE_RUN_STATUS_RUNNING", "CASCADE_RUN_STATUS_BUSY"} {
		if !(Conversation{Status: status}).IsRunning() {
			t.Fatalf("%q should be running", status)
		}
	}
	if (Conversation{Status: "CASCADE_RUN_STATUS_IDLE"}).IsRunning() {
		t.Fatal("idle should not be running")
	}
}
