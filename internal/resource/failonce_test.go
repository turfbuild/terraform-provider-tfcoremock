// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package resource

import (
	"testing"
)

// TestForcedFailureStatic proves the default behavior is unchanged: without a
// strike directory a listed id fails every call, and an unlisted id never
// fails.
func TestForcedFailureStatic(t *testing.T) {
	r := Resource{FailOnCreate: []string{"doomed"}}
	for i := 0; i < 3; i++ {
		if !r.forcedFailure(r.FailOnCreate, "create", "doomed") {
			t.Fatalf("call %d: a listed id must fail every call without fail_once", i+1)
		}
	}
	if r.forcedFailure(r.FailOnCreate, "create", "innocent") {
		t.Fatal("an unlisted id must never fail")
	}
}

// TestForcedFailureOnce proves the one-shot semantics: the first triggered
// call fails and records a strike, later calls succeed, and the strike is
// scoped to the (operation, id) pair — a different operation or id still gets
// its own first failure. A second Resource value over the same directory (a
// stand-in for a second provider process) observes the strike too.
func TestForcedFailureOnce(t *testing.T) {
	dir := t.TempDir()
	r := Resource{
		FailOnCreate: []string{"doomed", "other"},
		FailOnUpdate: []string{"doomed"},
		FailOnceDir:  dir,
	}

	if !r.forcedFailure(r.FailOnCreate, "create", "doomed") {
		t.Fatal("the first triggered call must fail")
	}
	if r.forcedFailure(r.FailOnCreate, "create", "doomed") {
		t.Fatal("the second call must see the strike and succeed")
	}
	if !r.forcedFailure(r.FailOnUpdate, "update", "doomed") {
		t.Fatal("a different operation on the same id gets its own first failure")
	}
	if !r.forcedFailure(r.FailOnCreate, "create", "other") {
		t.Fatal("a different id gets its own first failure")
	}

	second := Resource{FailOnCreate: []string{"doomed"}, FailOnceDir: dir}
	if second.forcedFailure(second.FailOnCreate, "create", "doomed") {
		t.Fatal("a second provider over the same directory must observe the strike")
	}
}

// TestForcedFailureDataSourceIsItsOwnOperation proves a data source read
// injects independently of a managed read: the two share fail_on_read but not
// the strike, so a fixture that fails a data read of an id does not silently
// spend the one-shot a managed read of the same id would have used.
func TestForcedFailureDataSourceIsItsOwnOperation(t *testing.T) {
	dir := t.TempDir()
	d := DataSource{FailOnRead: []string{"shared"}, FailOnceDir: dir}
	r := Resource{FailOnRead: []string{"shared"}, FailOnceDir: dir}

	if !forcedFailure(d.FailOnRead, d.FailOnceDir, "read-data", "shared") {
		t.Fatal("the data source read's first triggered call must fail")
	}
	if forcedFailure(d.FailOnRead, d.FailOnceDir, "read-data", "shared") {
		t.Fatal("the data source read's second call must see the strike and succeed")
	}
	if !r.forcedFailure(r.FailOnRead, "read", "shared") {
		t.Fatal("the managed read still has its own first failure to spend")
	}
}
