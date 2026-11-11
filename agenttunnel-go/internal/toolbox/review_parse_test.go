package toolbox

import "testing"

func TestParseCommandLineReviewCalls(t *testing.T) {
	comment, ok := ParseCommandLine(`$AT_TOOL tool review-comment --file internal/a.go --line 12 --end-line 14 --side new --text "guard the nil map"`)
	if !ok || comment.Kind != KindReviewComment || comment.File != "internal/a.go" ||
		comment.Line != 12 || comment.EndLine != 14 || comment.Side != "new" || comment.Text != "guard the nil map" {
		t.Fatalf("comment = %+v ok=%v", comment, ok)
	}

	reply, ok := ParseCommandLine(`$AT_TOOL tool review-reply --thread review-cmd-7 --text "added the guard"`)
	if !ok || reply.Kind != KindReviewReply || reply.Thread != "review-cmd-7" || reply.Text != "added the guard" {
		t.Fatalf("reply = %+v ok=%v", reply, ok)
	}

	// --reopen is a switch: it must not swallow the flag after it.
	reopen, ok := ParseCommandLine(`tool review-resolve --reopen --thread review-cmd-7 --text "still broken"`)
	if !ok || reopen.Kind != KindReviewResolve || !reopen.Reopen || reopen.Thread != "review-cmd-7" || reopen.Text != "still broken" {
		t.Fatalf("reopen = %+v ok=%v", reopen, ok)
	}

	// A bare resolve needs only the thread.
	resolve, ok := ParseCommandLine(`tool review-resolve --thread review-cmd-7`)
	if !ok || resolve.Kind != KindReviewResolve || resolve.Reopen {
		t.Fatalf("resolve = %+v ok=%v", resolve, ok)
	}

	for _, line := range []string{
		`tool review-comment --line 3 --text "no file"`,
		`tool review-comment --file a.go --text "no line"`,
		`tool review-comment --file a.go --line x --text "bad line"`,
		`tool review-comment --file a.go --line 3`,
		`tool review-reply --text "no thread"`,
		`tool review-reply --thread review-cmd-7`,
		`tool review-resolve`,
	} {
		if call, ok := ParseCommandLine(line); ok {
			t.Fatalf("%q parsed as %+v, want rejected", line, call)
		}
	}
}

func TestReviewKindsAreReviewCalls(t *testing.T) {
	for _, k := range []Kind{KindReviewComment, KindReviewReply, KindReviewResolve} {
		if !k.IsReview() {
			t.Fatalf("%s is not a review call", k)
		}
	}
	if KindNote.IsReview() || KindMessage.IsReview() {
		t.Fatal("note or message reported as a review call")
	}
}
