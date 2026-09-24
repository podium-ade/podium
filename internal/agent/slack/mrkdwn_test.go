package slack

import "testing"

func TestToMrkdwn(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// The reported bug: a report's labels arrived as literal asterisks.
			name: "bold label",
			in:   "**Answer:** the hold self-healed.",
			want: "*Answer:* the hold self-healed.",
		},
		{"underscore bold", "__Cause:__ a missing branch", "*Cause:* a missing branch"},
		{"two on one line", "**a** and **b**", "*a* and *b*"},
		{
			name: "link",
			in:   "see [ENG-16131](https://linear.app/x/ENG-16131) for the ticket",
			want: "see <https://linear.app/x/ENG-16131|ENG-16131> for the ticket",
		},
		{"heading becomes bold", "## Conclusion\nit was fine", "*Conclusion*\nit was fine"},
		{
			// Single asterisks are emphasis in both dialects; guessing which was meant is
			// worse than leaving it.
			name: "single asterisk is left alone",
			in:   "*emphasis* stays",
			want: "*emphasis* stays",
		},
		{
			// The whole point of skipping code: a SQL string full of * is data.
			name: "code fence is untouched",
			in:   "before\n```sql\nSELECT **x** FROM t WHERE a = '[y](z)'\n```\nafter **b**",
			want: "before\n```sql\nSELECT **x** FROM t WHERE a = '[y](z)'\n```\nafter *b*",
		},
		{
			name: "inline code is untouched",
			in:   "the column is `a**b` and **that** matters",
			want: "the column is `a**b` and *that* matters",
		},
		{
			// An unclosed fence is a truncated message; treating the rest as code is the
			// reading that cannot corrupt it.
			name: "unclosed fence swallows the rest",
			in:   "text **x**\n```\nSELECT **y**",
			want: "text *x*\n```\nSELECT **y**",
		},
		{
			// A lone backtick in prose must not eat the paragraph after it.
			name: "lone backtick stays prose",
			in:   "it costs 5` and **b** follows",
			want: "it costs 5` and *b* follows",
		},
		{"plain text is unchanged", "nothing to do here", "nothing to do here"},
		{"empty", "", ""},
		{
			// Bullets are left alone: Slack renders a hyphen list perfectly well.
			name: "bullets survive",
			in:   "- **one** thing\n- two",
			want: "- *one* thing\n- two",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := toMrkdwn(c.in); got != c.want {
				t.Errorf("toMrkdwn(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}
