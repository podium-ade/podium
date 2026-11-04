package slack

import "testing"

func TestFromMrkdwn(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "hello there", "hello there"},
		{"bold", "this is *important*", "this is **important**"},
		{"italic", "this is _subtle_", "this is *subtle*"},
		{"strike", "this is ~gone~", "this is ~~gone~~"},
		{"bold and italic", "*a* and _b_", "**a** and *b*"},
		{"adjacent spans", "*a* *b*", "**a** **b**"},
		{"punctuation after", "do *this*, then _that_.", "do **this**, then *that*."},
		{"arithmetic is not bold", "2 * 3 * 4", "2 * 3 * 4"},
		{"snake_case is not italic", "set my_var_name now", "set my_var_name now"},
		{"labelled link", "see <https://example.com/x|the doc>", "see [the doc](https://example.com/x)"},
		{"bare link", "see <https://example.com/x>", "see https://example.com/x"},
		{"link labelled with itself", "<https://a.b|https://a.b>", "https://a.b"},
		{"mailto", "mail <mailto:a@b.c|a@b.c>", "mail a@b.c"},
		{"channel", "in <#C123ABC|general>", "in #general"},
		{"channel without name", "in <#C123ABC>", "in #C123ABC"},
		{"here", "<!here> look", "@here look"},
		{"subteam", "ping <!subteam^S123|@eng>", "ping @eng"},
		{"escapes", "a &lt;b&gt; &amp; c", "a <b> & c"},
		{"code span untouched", "run `*not bold*` please", "run `*not bold*` please"},
		{"code block untouched", "```\n*x* _y_ &lt;\n```\n*z*", "```\n*x* _y_ &lt;\n```\n**z**"},
		{"unclosed backtick is prose", "a `b *c*", "a `b **c**"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fromMrkdwn(tc.in); got != tc.want {
				t.Fatalf("fromMrkdwn(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBeforeTS(t *testing.T) {
	if !beforeTS("1700000000.000100", "1700000000.000200") {
		t.Fatal("an earlier micro must be before")
	}
	if beforeTS("1700000000.000200", "1700000000.000200") {
		t.Fatal("a ts is not before itself")
	}
}
