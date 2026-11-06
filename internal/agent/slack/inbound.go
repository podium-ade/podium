package slack

import (
	"regexp"
	"strings"
)

// Slack writes mrkdwn, not Markdown: *bold*, _italic_, ~strike~, <url|label> links and
// angle-bracket escapes for channels and special mentions. Podium stores Markdown for every
// surface (decision 19), so what a person typed in Slack is converted on the way in and reads
// in the web chat the way it read in Slack.
//
// The conversion is deliberately conservative. Code — a ``` block or a `span` — is copied
// through unformatted, because inside code an asterisk is an asterisk. Slack's three entity
// escapes are undone there too: Slack escapes <, > and & everywhere, code included, so
// `a &lt; b` is what arrives for the a < b somebody typed. Outside code a marker is
// only converted where Slack itself would have formatted it: hugging a word, not floating in
// a sentence like "2 * 3 * 4".

var (
	// <https://x|label>, <https://x>, <mailto:a@b|a@b>.
	inLinkRE = regexp.MustCompile(`<((?:https?|mailto):[^|>\s]+)(?:\|([^>]*))?>`)
	// <#C123|general> or <#C123>.
	inChannelRE = regexp.MustCompile(`<#([CG][A-Z0-9]+)(?:\|([^>]*))?>`)
	// <!here>, <!channel>, <!everyone>, <!subteam^S123|@team>, <!date^...|fallback>.
	inSpecialRE = regexp.MustCompile(`<!([a-z]+)(?:\^[^|>]*)?(?:\|([^>]*))?>`)

	inBoldRE   = regexp.MustCompile(`(^|[\s(\[{"'])\*([^\s*](?:[^*\n]*[^\s*])?)\*($|[\s)\]}.,!?:;"'])`)
	inItalicRE = regexp.MustCompile(`(^|[\s(\[{"'])_([^\s_](?:[^_\n]*[^\s_])?)_($|[\s)\]}.,!?:;"'])`)
	inStrikeRE = regexp.MustCompile(`(^|[\s(\[{"'])~([^\s~](?:[^~\n]*[^\s~])?)~($|[\s)\]}.,!?:;"'])`)
)

// fromMrkdwn converts one Slack message's mrkdwn into Markdown. It is the inbound half of
// toMrkdwn (mrkdwn.go), which converts what the bot says on the way out. User mentions are not
// its business: the source has already turned <@U123> into @name, which needs an API call.
func fromMrkdwn(text string) string {
	if text == "" {
		return ""
	}
	var b strings.Builder
	for _, part := range splitInboundCode(text) {
		if part.code {
			b.WriteString(unescapeSlack(part.text))
			continue
		}
		b.WriteString(fromMrkdwnProse(part.text))
	}
	return b.String()
}

func fromMrkdwnProse(s string) string {
	s = inLinkRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := inLinkRE.FindStringSubmatch(m)
		url, label := sub[1], sub[2]
		if label == "" || label == url || label == strings.TrimPrefix(url, "mailto:") {
			return strings.TrimPrefix(url, "mailto:")
		}
		return "[" + label + "](" + url + ")"
	})
	s = inChannelRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := inChannelRE.FindStringSubmatch(m)
		if sub[2] != "" {
			return "#" + sub[2]
		}
		return "#" + sub[1]
	})
	s = inSpecialRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := inSpecialRE.FindStringSubmatch(m)
		if sub[2] != "" {
			return sub[2]
		}
		return "@" + sub[1]
	})
	// Bold first, into a placeholder that italic cannot match, then italic and strike.
	// Markdown's italic is a single asterisk, which is Slack's bold, so converting in the
	// other order would turn *bold* into italic and then into nonsense.
	s = replaceMarkers(inBoldRE, s, "\x00\x00$2\x00\x00")
	s = replaceMarkers(inItalicRE, s, "*$2*")
	s = replaceMarkers(inStrikeRE, s, "~~$2~~")
	s = strings.ReplaceAll(s, "\x00\x00", "**")
	return unescapeSlack(s)
}

// unescapeSlack undoes the three escapes Slack applies to message text; everything else
// arrived literally. &amp; goes last, so "&amp;lt;" (somebody typing the entity itself) reads
// back as "&lt;" rather than "<".
func unescapeSlack(s string) string {
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	return strings.ReplaceAll(s, "&amp;", "&")
}

// replaceMarkers applies a marker pattern until it stops matching. The patterns consume the
// whitespace on each side, so two adjacent spans ("*a* *b*") need a second pass for the one
// whose leading space the first match ate.
func replaceMarkers(re *regexp.Regexp, s, inner string) string {
	repl := "${1}" + inner + "${3}"
	for i := 0; i < 4; i++ {
		next := re.ReplaceAllString(s, repl)
		if next == s {
			return s
		}
		s = next
	}
	return s
}

type inboundPart struct {
	text string
	code bool
}

// splitInboundCode cuts text into prose and code, keeping the fences and backticks with the code.
// An unclosed fence or backtick is prose: Slack renders it literally, and so should we.
func splitInboundCode(text string) []inboundPart {
	var out []inboundPart
	for text != "" {
		i := strings.IndexByte(text, '`')
		if i < 0 {
			out = append(out, inboundPart{text: text})
			break
		}
		if i > 0 {
			out = append(out, inboundPart{text: text[:i]})
			text = text[i:]
		}
		fence := "`"
		if strings.HasPrefix(text, "```") {
			fence = "```"
		}
		end := strings.Index(text[len(fence):], fence)
		if end < 0 {
			out = append(out, inboundPart{text: text})
			break
		}
		n := len(fence) + end + len(fence)
		out = append(out, inboundPart{text: text[:n], code: true})
		text = text[n:]
	}
	return out
}
