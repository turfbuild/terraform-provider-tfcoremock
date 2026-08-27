// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package resource

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// fakePrivate stands in for the framework's private-state type, which lives in
// an internal package and so cannot be constructed from here.
type fakePrivate struct {
	values map[string][]byte
	diags  diag.Diagnostics
}

func (f fakePrivate) GetKey(_ context.Context, key string) ([]byte, diag.Diagnostics) {
	return f.values[key], f.diags
}

func TestEphemeralRecordAccumulatesInOrder(t *testing.T) {
	dir := t.TempDir()
	res := EphemeralResource{Name: "tfcoremock_ephemeral_secret", AuditDirectory: dir}

	// Two opens of the same id, as a client that opens once per walk produces.
	for _, event := range []string{"open", "renew", "close", "open", "close"} {
		if err := res.record("token", event); err != nil {
			t.Fatalf("record(%q): %v", event, err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "token.json"))
	if err != nil {
		t.Fatalf("reading the record: %v", err)
	}
	var audit EphemeralAudit
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatalf("unmarshalling the record: %v", err)
	}

	if audit.Id != "token" {
		t.Errorf("id = %q, want %q", audit.Id, "token")
	}
	want := []string{"open", "renew", "close", "open", "close"}
	if len(audit.Events) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(audit.Events), len(want), audit.Events)
	}
	for i, event := range want {
		if audit.Events[i].Event != event {
			t.Errorf("event %d = %q, want %q", i, audit.Events[i].Event, event)
		}
		if audit.Events[i].Sequence != i+1 {
			t.Errorf("event %d has sequence %d, want %d", i, audit.Events[i].Sequence, i+1)
		}
	}
}

func TestEphemeralRecordIsDisabledWithoutADirectory(t *testing.T) {
	// use_only_state leaves no resource directory. Recording has to become a
	// no-op rather than an error, or every open under that mode would fail.
	res := EphemeralResource{Name: "tfcoremock_ephemeral_secret"}
	if err := res.record("token", "open"); err != nil {
		t.Fatalf("record with no audit directory: %v", err)
	}
}

func TestEphemeralIdentifyRejectsAPrivateStateThatDidNotComeBack(t *testing.T) {
	res := EphemeralResource{Name: "tfcoremock_ephemeral_secret"}
	good, err := json.Marshal(ephemeralPrivate{Id: "token"})
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	tests := map[string]struct {
		private ephemeralPrivateReader
		wantID  string
		wantOK  bool
	}{
		"round-tripped": {
			private: fakePrivate{values: map[string][]byte{ephemeralPrivateKey: good}},
			wantID:  "token",
			wantOK:  true,
		},
		// The defect this exists to catch: a client that drops the blob
		// entirely still gets a well-formed renew and close on the wire, so
		// without the assertion it is indistinguishable from a correct one.
		"nothing sent back": {
			private: nil,
		},
		"sent back without the entry": {
			private: fakePrivate{values: map[string][]byte{}},
		},
		"entry is not what was stored": {
			private: fakePrivate{values: map[string][]byte{ephemeralPrivateKey: []byte("{")}},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var diags diag.Diagnostics
			id, ok := res.identify(context.Background(), test.private, &diags)
			if ok != test.wantOK {
				t.Fatalf("ok = %v, want %v (diags: %v)", ok, test.wantOK, diags)
			}
			if id != test.wantID {
				t.Errorf("id = %q, want %q", id, test.wantID)
			}
			if !test.wantOK && !diags.HasError() {
				t.Error("no error diagnostic was raised")
			}
		})
	}
}
