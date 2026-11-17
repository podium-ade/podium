#!/usr/bin/env bash
# seed.sh — Fernhill Cycle Co., a fictional online bicycle-parts shop, seeded onto the nested
# stack through the CLI and the Connect API (never SQL), so a seed failure is itself a finding
# about the branch. Run through `stack.sh seed`. jq, not Python: the -dev image has no Python.
#
# Idempotent for config (already_exists is success). Tasks and conversations are history and
# cost tokens, so they are created once per stack (marker files in the state dir).
set -uo pipefail

state=${STACK_STATE:?run through stack.sh seed}
root=${STACK_ROOT:?}
skill=$(cd "$(dirname "$0")" && pwd)
envf=$state/.env
getenv() { sed -n "s/^$1=//p" "$envf" | tail -1; }
server=$(getenv PODIUM_SERVER)
token=$(getenv PODIUM_LOCAL_TOKEN)
cli() { "$root/bin/podium" --server "$server" --token "$token" "$@"; }
# Seeded turns run on the cheapest real model. STACK_MODEL to change it.
model=${STACK_MODEL:-claude-haiku-5-5}

ok=0 failed=0
report() {
  if [ "$2" = ok ]; then ok=$((ok + 1)); printf '  ✓ %s\n' "$1"
  else failed=$((failed + 1)); printf '  ✗ %s — %s\n' "$1" "${3:-}" >&2; fi
}
# rpc <Service> <Method>, JSON body on stdin → response body; already_exists is success.
rpc() {
  local out code
  out=$(curl -sS -m 60 -w '\n%{http_code}' -X POST "$server/$1/$2" \
    -H 'content-type: application/json' -H "authorization: Bearer $token" --data-binary @-)
  code=${out##*$'\n'}; out=${out%$'\n'*}
  if [ "$code" = 200 ] || grep -q '"code":"already_exists"' <<<"$out"; then printf '%s' "$out"; return 0; fi
  printf '%s' "$code $out" >&2; return 1
}
agent() { rpc podium.agent.v1.AgentService "$1" <<<"$2"; }
seed() { local err; if err=$(agent "$2" "$3" 2>&1 >/dev/null); then report "$1" ok; else report "$1" fail "$err"; fi; }

echo "seeding Fernhill Cycle Co. at $server"

echo "model provider"
key_set() { agent GetSettings '{}' 2>/dev/null |
  jq -r '[.providers[]? | select(.provider=="anthropic" and (.keySet // .key_set))] | if length>0 then 1 else 0 end' 2>/dev/null || echo 0; }
# This turn's own key, sent only to the nested stack, which stores it encrypted under its own
# throwaway master key. Never printed: it goes on stdin.
if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
  if err=$(jq -n '{provider:"anthropic", key:env.ANTHROPIC_API_KEY, global:true}' |
      rpc podium.agent.v1.AgentService SetProviderKey 2>&1 >/dev/null); then
    report "anthropic key (from this turn)" ok
  else report "anthropic key" fail "$err"; fi
elif [ "$(key_set)" = 1 ]; then report "anthropic key (already set)" ok
else report "anthropic key" fail "no ANTHROPIC_API_KEY in this turn — conversations skipped"; fi
have_key=$(key_set)

echo "secrets (placeholder values)"
for s in fernhill.shop_api_token fernhill.supplier_api_key fernhill.warehouse_db_password fernhill.slack_webhook; do
  if printf 'placeholder-%s' "$s" | cli secret set "$s" >/dev/null 2>&1; then report "secret $s" ok
  else report "secret $s" fail "podium secret set failed"; fi
done

echo "personalities"
seed "personality fern (support)" CreatePersonality '{
  "name":"fern","display_name":"Fern — Customer Support",
  "instructions":"You answer for Fernhill Cycle Co., an online bicycle-parts shop in Vancouver. Be warm and brief. Never promise a refund or a delivery date you have not checked; say who will confirm it."}'
seed "personality ledger (finance)" CreatePersonality '{
  "name":"ledger","display_name":"Ledger — Finance Reviewer",
  "instructions":"You review Fernhill numbers skeptically: margins, refunds, supplier invoices. Show the arithmetic. Flag anything over CA$500 for Priya Nair (finance lead) to approve."}'

echo "playbooks"
seed "playbook order-triage" CreatePlaybook '{"playbook":{
  "name":"order-triage","image":"podium-agent-runtime:dev",
  "system_prompt":"Triage overnight orders for Fernhill Cycle Co.: find orders stuck in payment review, out-of-stock lines and address problems; summarize what support (Sam Okafor) must act on today.",
  "allowed_tools":["read","grep","glob","bash"],"max_turns":30,"timeout":"15m",
  "secrets":[{"name":"fernhill.shop_api_token","target":"env","key":"SHOP_API_TOKEN"}]}}'
seed "playbook inventory-reorder" CreatePlaybook '{"playbook":{
  "name":"inventory-reorder","image":"podium-agent-runtime:dev",
  "system_prompt":"Compare stock levels to the 30-day sales rate and draft purchase orders for anything under three weeks of cover. Group by supplier. Do not submit; draft only.",
  "allowed_tools":["read","grep","glob","bash"],"max_turns":30,"timeout":"20m",
  "secrets":[{"name":"fernhill.supplier_api_key","target":"env","key":"SUPPLIER_API_KEY"},
             {"name":"fernhill.warehouse_db_password","target":"env","key":"WAREHOUSE_DB_PASSWORD"}]}}'
seed "playbook refund-review" CreatePlaybook "$(jq -n --arg p "$(printf 'Review each refund request against the returns policy: within 30 days, unused, original packaging; damaged-in-transit claims need a photo. %.0s' 1 2 3 4 5 6)Escalate anything over CA\$500 to finance." \
  '{playbook:{name:"refund-review",image:"podium-agent-runtime:dev",system_prompt:$p,allowed_tools:["read","bash"],max_turns:40,timeout:"30m"}}')"
# Only files a turn's final message names are attached, so the prompt makes it repeat the list
# the command prints — every name, exactly, including the 120-character one.
charts_prompt='You make Fernhill Cycle Co. charts. Run exactly this command with bash, once: node -e "$CHARTS_JS" — it prints the files it wrote, one per line. Then reply with one short sentence followed by every one of those file names, exactly as printed, one per line. Only files you name are attached, so name every one. Do nothing else.'
# The playbook the attachments conversation delegates to: a real turn in the branch's runtime
# image that writes three PNG charts of different shapes and a CSV as its artifacts.
seed "playbook sales-charts" CreatePlaybook "$(jq -n --rawfile js "$skill/charts.js" --arg prompt "$charts_prompt" '{playbook:{
  name:"sales-charts", image:"podium-agent-runtime:dev",
  system_prompt:$prompt, allowed_tools:["bash"], max_turns:6, timeout:"10m", env:{CHARTS_JS:$js}}}')"
seed "playbook quarterly-charts" CreatePlaybook "$(jq -n --rawfile js "$skill/charts.js" --arg prompt "$charts_prompt" '{playbook:{
  name:"quarterly-charts", image:"podium-agent-runtime:dev",
  system_prompt:$prompt, allowed_tools:["bash"], max_turns:6, timeout:"10m",
  env:{CHARTS_JS:$js, CHARTS_PACK:"quarterly"}}}')"

echo "routines"
routine_id() { agent ListRoutines '{}' 2>/dev/null | jq -r --arg n "$1" '.routines[]? | select(.name==$n) | .id' | head -1; }
routine() { # routine <name> <draft-json> [pause]
  local id out
  if [ -n "$(routine_id "$1")" ]; then report "routine $1 (exists)" ok; return; fi
  if out=$(agent CreateRoutine "{\"draft\":$2}" 2>&1); then
    report "routine $1" ok
    if [ "${3:-}" = pause ]; then
      id=$(jq -r '.routine.id // empty' <<<"$out")
      agent PauseRoutine "{\"id\":\"$id\"}" >/dev/null 2>&1 && report "  …paused" ok || report "  …paused" fail PauseRoutine
    fi
  else report "routine $1" fail "$out"; fi
}
routine "Morning order triage" "{\"name\":\"Morning order triage\",\"schedule\":\"0 8 * * 1-5\",\"timezone\":\"America/Vancouver\",
  \"instruction\":\"Triage every order placed overnight and list what support has to handle before 10am.\",\"model\":\"$model\",\"daily_cap_usd\":1}"
routine "Weekly inventory reorder" "{\"name\":\"Weekly inventory reorder\",\"schedule\":\"0 15 * * 5\",\"timezone\":\"America/Vancouver\",
  \"instruction\":\"Draft next week's purchase orders from stock levels and sales.\",\"model\":\"$model\",\"daily_cap_usd\":2,
  \"notes\":\"Paused until the new supplier (Coastline Components) is onboarded. — Priya\"}" pause

echo "conversations (answered by the real model, $model)"
chats=$state/seeded-chats
if [ -f "$chats" ]; then echo "  (already seeded — $chats)"
elif [ "$have_key" != 1 ]; then report "conversations" fail "no model key — skipped"
else
  converse() { # converse <title> <message>
    local c id err
    c=$(agent CreateChat "$(jq -n --arg t "$1" '{title:$t}')" 2>/dev/null)
    id=$(jq -r '.chat.id // empty' <<<"$c" 2>/dev/null)
    [ -n "$id" ] || { report "chat $1" fail "CreateChat: $c"; return; }
    if err=$(jq -n --arg c "$id" --arg t "$2" --arg m "$model" '{chat_id:$c,text:$t,model:$m}' |
        rpc podium.agent.v1.AgentService SendChatMessage 2>&1 >/dev/null); then report "chat $1 ($id)" ok
    else report "chat $1" fail "$err"; fi
  }
  converse "Black Friday email" \
    "We're Fernhill Cycle Co., an online bike-parts shop. Draft three subject lines for our Black Friday email: 25% off drivetrain parts, free shipping over CA\$100. Keep each under 50 characters."
  converse "Late order from Kelowna" \
    "A customer in Kelowna says order FH-10482 (a Shimano 105 cassette) hasn't moved in six days. Draft a short, honest reply that apologises, says we're checking with the courier, and promises an update by tomorrow — without promising a delivery date."
  # Image attachments: the assistant delegates to sales-charts, whose task writes three PNGs and
  # a CSV; the conductor attaches them to the reply. The answer arrives a minute or two later.
  converse "Weekly sales charts" \
    "Delegate this to the sales-charts playbook: make this week's sales charts."
  # Many images: eight shapes, a 120-character name and a .png that is not an image — what a
  # gallery, its lightbox and its broken-image fallback have to survive.
  converse "Quarterly chart pack" \
    "Delegate this to the quarterly-charts playbook: make the quarterly chart pack."
  date -u +%FT%TZ >"$chats"
fi

echo "tasks (real containers in dind)"
marker=$state/seeded-tasks
if [ -f "$marker" ]; then echo "  (already seeded — $marker)"
else
  run() {
    local label=$1; shift; local out
    task_id=""
    if out=$(cli run --detach --label nested "$@" 2>&1) && task_id=$(grep -oE 'task_[0-9a-z]+' <<<"$out" | head -1) && [ -n "$task_id" ]; then
      report "task $label ($task_id)" ok
    else report "task $label" fail "$out"; fi
  }
  A=/workspace/.podium/artifacts
  run daily-sales-report --image alpine:3 -- sh -c "mkdir -p $A
    echo 'order,sku,item,qty,total_cad' > $A/sales.csv
    i=10400; for p in 'CS-105-11|Shimano 105 cassette 11-32|89.00' 'CH-KMC-X11|KMC X11 chain|42.50' 'TB-700-28|700x28c tube|8.99'; do
      i=\$((i+1)); sku=\${p%%|*}; r=\${p#*|}; item=\${r%|*}; price=\${r##*|}
      echo \"FH-\$i,\$sku,\$item,1,\$price\" >> $A/sales.csv; echo \"processed FH-\$i \$sku\"; sleep 1
    done
    printf 'Fernhill daily sales\n3 orders, CA\$140.49\n' > $A/summary.txt; echo report written"
  run supplier-sync --image alpine:3 -- sh -c 'echo "syncing stock from Coastline Components…"; echo "GET /v2/stock → 503 Service Unavailable" >&2; exit 3'
  run price-check --image alpine:3 --env CURRENCY=CAD --env MARGIN_FLOOR=0.22 -- sh -c 'echo "checking prices in $CURRENCY against a margin floor of $MARGIN_FLOOR"'
  run order-export --image alpine:3 -- sh -c 'i=0; while [ $i -lt 400 ]; do echo "export FH-$((11000+i)) status=shipped carrier=canada-post"; i=$((i+1)); done'
  run webhook-listener --image alpine:3 --timeout 6h -- sh -c 'while true; do echo "$(date +%T) waiting for order webhooks…"; sleep 5; done'
  run catalogue-backfill --image alpine:3 -- sh -c 'echo "backfilling 12,000 product images…"; sleep 3600'; cancel_id=$task_id
  if [ -n "$cancel_id" ]; then
    sleep 3
    cli task cancel "$cancel_id" >/dev/null 2>&1 && report "  …cancelled $cancel_id" ok || report "  …cancel" fail "podium task cancel"
  fi
  date -u +%FT%TZ >"$marker"
fi

echo
echo "seeded: $ok ok, $failed failed"
[ "$failed" = 0 ]
