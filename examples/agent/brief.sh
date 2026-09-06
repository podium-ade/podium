#!/bin/sh
# brief.sh prints a base64 turn brief for PODIUM_AGENT_TURN.
#
#   ./bin/podium run --image podium-agent-runtime:dev \
#     --env PODIUM_AGENT_DRY_RUN=1 \
#     --env PODIUM_AGENT_TURN=$(examples/agent/brief.sh "hello")
#
# The brief is the smallest one that validates: a chat source, the podium profile, the
# general skill on Anthropic, an empty transcript, no repos and no memory.
# agent/runtime/src/brief.ts is the schema; agent/runtime/testdata/brief.example.json is the
# full one, which is a Grok turn so that every field has a value somewhere.
#
# The JSON is compact and its keys are in the schema's order, because a Go test asserts
# this script and its own encoder produce the same bytes for the same instruction.
set -eu

if [ "$#" -eq 0 ]; then
	echo "usage: $0 INSTRUCTION" >&2
	exit 2
fi

instruction="$1"

json=$(jq -cn --arg instruction "$instruction" '{
	version: 1,
	session_id: "sess_00000000000000000000000000",
	turn_id: "turn_00000000000000000000000000",
	source: {kind: "chat", ref: "chat_00000000000000000000000000"},
	profile: {
		name: "podium",
		display_name: "Podium",
		system_prompt: "You are Podium, an agent that runs on Podium.",
		model: "claude-opus-5"
	},
	skill: {
		name: "general",
		system_prompt: "Answer the question. Use the tools you have to check before you answer.",
		allowed_tools: ["read", "grep", "glob", "bash"],
		max_turns: 20
	},
	transcript: [],
	transcript_truncated: false,
	instruction: $instruction,
	provider: {id: "anthropic", api_key_env: "ANTHROPIC_API_KEY"}
}')

printf '%s' "$json" | base64 | tr -d '\n'
